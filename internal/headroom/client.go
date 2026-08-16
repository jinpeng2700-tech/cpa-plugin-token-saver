// Package headroom provides the bounded, fail-open client and protocol adapter
// for the local Headroom compression service.
package headroom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	maxPayloadBytes     = 1 << 20
	minTimeout          = 100 * time.Millisecond
	maxTimeout          = 1500 * time.Millisecond
	failureWindow       = 60 * time.Second
	circuitOpenDuration = 60 * time.Second
	probeCacheDuration  = 5 * time.Second
	compressPath        = "/v1/compress"
)

var errUnsafePeer = errors.New("headroom dial target or peer is not loopback")

// Outcome is a stable, low-cardinality result suitable for aggregate metrics.
// It deliberately excludes URLs, models, status codes, and response details.
type Outcome string

const (
	OutcomeApplied              Outcome = "applied"
	OutcomeNoChange             Outcome = "no_change"
	OutcomeUnsupportedFormat    Outcome = "bypass_unsupported_format"
	OutcomeUnsupportedStructure Outcome = "bypass_unsupported_structure"
	OutcomeRequestTooLarge      Outcome = "bypass_request_too_large"
	OutcomeSaturated            Outcome = "bypass_saturated"
	OutcomeCircuitOpen          Outcome = "bypass_circuit_open"
	OutcomeTimeout              Outcome = "fail_timeout"
	OutcomeConnection           Outcome = "fail_connection"
	OutcomeNetwork              Outcome = "fail_network"
	OutcomeRedirect             Outcome = "fail_redirect"
	OutcomeHTTPStatus           Outcome = "fail_http_status"
	OutcomeResponseEncoding     Outcome = "fail_response_encoding"
	OutcomeResponseTooLarge     Outcome = "fail_response_too_large"
	OutcomeInvalidJSON          Outcome = "fail_invalid_json"
	OutcomeInvalidResponse      Outcome = "fail_invalid_response"
)

type dialContextFunc func(context.Context, string, string) (net.Conn, error)

type clientOptions struct {
	now  func() time.Time
	dial dialContextFunc
}

type clientOption func(*clientOptions)

func withNow(now func() time.Time) clientOption {
	return func(options *clientOptions) { options.now = now }
}

func withDialContext(dial dialContextFunc) clientOption {
	return func(options *clientOptions) { options.dial = dial }
}

// Client owns an HTTP transport, a one-slot concurrency guard, and the circuit
// state for one validated Headroom endpoint/configuration snapshot.
type Client struct {
	endpoint   *url.URL
	timeout    time.Duration
	httpClient *http.Client
	semaphore  chan struct{}
	now        func() time.Time
	circuit    circuitBreaker
	probeMu    sync.Mutex
	probeAt    time.Time
	probeValue Outcome
}

// Probe performs a small compression-only request and caches the low-cardinality
// result briefly. It uses the same bounded client and never retries.
func (client *Client) Probe(ctx context.Context) Outcome {
	if client == nil {
		return OutcomeNetwork
	}
	client.probeMu.Lock()
	defer client.probeMu.Unlock()
	now := client.now()
	if !client.probeAt.IsZero() && now.Sub(client.probeAt) < probeCacheDuration {
		return client.probeValue
	}
	request := []byte(`{"messages":[{"role":"user","content":"ping"}],"model":"headroom-health-probe"}`)
	outcome := client.Compress(ctx, request, func(messages []json.RawMessage) error {
		if len(messages) != 1 {
			return fmt.Errorf("probe message count changed")
		}
		var message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if errDecode := json.Unmarshal(messages[0], &message); errDecode != nil || message.Role != "user" || message.Content == "" {
			return fmt.Errorf("probe response shape changed")
		}
		return nil
	})
	if outcome != OutcomeSaturated {
		client.probeAt = now
		client.probeValue = outcome
	}
	return outcome
}

// NewClient constructs a client bound to one literal loopback base URL. The
// final request path is always /v1/compress regardless of caller input.
func NewClient(baseURL string, timeout time.Duration) (*Client, error) {
	return newClient(baseURL, timeout)
}

func newClient(baseURL string, timeout time.Duration, options ...clientOption) (*Client, error) {
	endpoint, errEndpoint := fixedEndpoint(baseURL)
	if errEndpoint != nil {
		return nil, errEndpoint
	}
	if timeout < minTimeout || timeout > maxTimeout {
		return nil, fmt.Errorf("headroom timeout must be between %s and %s", minTimeout, maxTimeout)
	}

	settings := clientOptions{now: time.Now}
	for _, option := range options {
		option(&settings)
	}
	if settings.now == nil {
		settings.now = time.Now
	}
	if settings.dial == nil {
		dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
		settings.dial = dialer.DialContext
	}

	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           guardedDial(settings.dial),
		DisableCompression:    true,
		MaxIdleConns:          1,
		MaxIdleConnsPerHost:   1,
		MaxConnsPerHost:       1,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: timeout,
	}
	client := &Client{
		endpoint:  endpoint,
		timeout:   timeout,
		semaphore: make(chan struct{}, 1),
		now:       settings.now,
	}
	client.httpClient = &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return client, nil
}

// Compress performs one bounded call. The validator runs before the call is
// recorded as successful, so semantic invariant failures also feed the circuit.
func (client *Client) Compress(ctx context.Context, requestBody []byte, validate func([]json.RawMessage) error) Outcome {
	if client == nil || client.httpClient == nil || client.endpoint == nil {
		return OutcomeNetwork
	}
	if len(requestBody) == 0 || len(requestBody) > maxPayloadBytes {
		return OutcomeRequestTooLarge
	}

	now := client.now()
	probe, allowed := client.circuit.begin(now)
	if !allowed {
		return OutcomeCircuitOpen
	}
	select {
	case client.semaphore <- struct{}{}:
	case <-ctx.Done():
		client.circuit.cancel(probe)
		return classifyRequestError(ctx.Err())
	default:
		client.circuit.cancel(probe)
		return OutcomeSaturated
	}
	defer func() { <-client.semaphore }()

	callCtx, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()
	request, errRequest := http.NewRequestWithContext(callCtx, http.MethodPost, client.endpoint.String(), bytes.NewReader(requestBody))
	if errRequest != nil {
		client.circuit.finish(client.now(), probe, false)
		return OutcomeNetwork
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")

	response, errDo := client.httpClient.Do(request)
	if errDo != nil {
		client.circuit.finish(client.now(), probe, false)
		return classifyRequestError(errDo)
	}
	defer response.Body.Close()

	if response.StatusCode >= 300 && response.StatusCode < 400 {
		client.circuit.finish(client.now(), probe, false)
		return OutcomeRedirect
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		client.circuit.finish(client.now(), probe, false)
		return OutcomeHTTPStatus
	}
	encoding := strings.TrimSpace(response.Header.Get("Content-Encoding"))
	if encoding != "" && !strings.EqualFold(encoding, "identity") {
		client.circuit.finish(client.now(), probe, false)
		return OutcomeResponseEncoding
	}
	if response.ContentLength > maxPayloadBytes {
		client.circuit.finish(client.now(), probe, false)
		return OutcomeResponseTooLarge
	}

	raw, errRead := io.ReadAll(io.LimitReader(response.Body, maxPayloadBytes+1))
	if errRead != nil {
		client.circuit.finish(client.now(), probe, false)
		return classifyRequestError(errRead)
	}
	if len(raw) > maxPayloadBytes {
		client.circuit.finish(client.now(), probe, false)
		return OutcomeResponseTooLarge
	}
	messages, outcome := decodeMessages(raw)
	if outcome != OutcomeApplied {
		client.circuit.finish(client.now(), probe, false)
		return outcome
	}
	if validate != nil {
		if errValidate := validate(messages); errValidate != nil {
			client.circuit.finish(client.now(), probe, false)
			return OutcomeInvalidResponse
		}
	}
	client.circuit.finish(client.now(), probe, true)
	return OutcomeApplied
}

// CloseIdleConnections releases reusable sockets during reconfiguration and
// shutdown. Client request paths never start plugin-owned goroutines.
func (client *Client) CloseIdleConnections() {
	if client == nil || client.httpClient == nil || client.httpClient.Transport == nil {
		return
	}
	if closer, ok := client.httpClient.Transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func fixedEndpoint(raw string) (*url.URL, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return nil, fmt.Errorf("headroom URL must be non-empty without surrounding whitespace")
	}
	parsed, errParse := url.Parse(raw)
	if errParse != nil {
		return nil, fmt.Errorf("parse headroom URL: %w", errParse)
	}
	if parsed.Scheme != "http" || parsed.Opaque != "" || parsed.Host == "" {
		return nil, fmt.Errorf("headroom URL must use http with an authority")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, fmt.Errorf("headroom URL must not contain credentials, query, or fragment")
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" {
		return nil, fmt.Errorf("headroom URL must not configure a path")
	}
	host := parsed.Hostname()
	if host != "127.0.0.1" && host != "::1" {
		return nil, fmt.Errorf("headroom host must be the literal loopback address")
	}
	if port := parsed.Port(); port != "" {
		value, errPort := strconv.Atoi(port)
		if errPort != nil || value < 1 || value > 65535 {
			return nil, fmt.Errorf("headroom port must be between 1 and 65535")
		}
	}
	parsed.Path = compressPath
	parsed.RawPath = ""
	return parsed, nil
}

func guardedDial(base dialContextFunc) dialContextFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, errSplit := net.SplitHostPort(address)
		if errSplit != nil || !isLiteralLoopback(host) {
			return nil, errUnsafePeer
		}
		connection, errDial := base(ctx, network, address)
		if errDial != nil {
			return nil, errDial
		}
		if !isLoopbackPeer(connection.RemoteAddr()) {
			_ = connection.Close()
			return nil, errUnsafePeer
		}
		return connection, nil
	}
}

func isLiteralLoopback(host string) bool {
	return host == "127.0.0.1" || host == "::1"
}

func isLoopbackPeer(address net.Addr) bool {
	if address == nil {
		return false
	}
	if tcpAddress, ok := address.(*net.TCPAddr); ok {
		return tcpAddress.IP != nil && tcpAddress.IP.IsLoopback()
	}
	host, _, errSplit := net.SplitHostPort(address.String())
	if errSplit != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func classifyRequestError(err error) Outcome {
	if errors.Is(err, context.DeadlineExceeded) {
		return OutcomeTimeout
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return OutcomeTimeout
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return OutcomeConnection
	}
	if errors.Is(err, errUnsafePeer) {
		return OutcomeConnection
	}
	var operationError *net.OpError
	if errors.As(err, &operationError) && operationError.Op == "dial" {
		return OutcomeConnection
	}
	return OutcomeNetwork
}

func decodeMessages(raw []byte) ([]json.RawMessage, Outcome) {
	if !json.Valid(raw) {
		return nil, OutcomeInvalidJSON
	}
	if errUnique := validateUniqueObjectKeys(raw); errUnique != nil {
		return nil, OutcomeInvalidResponse
	}
	var envelope map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil || envelope == nil {
		return nil, OutcomeInvalidResponse
	}
	messagesRaw, exists := envelope["messages"]
	if !exists || len(messagesRaw) == 0 || bytes.Equal(bytes.TrimSpace(messagesRaw), []byte("null")) {
		return nil, OutcomeInvalidResponse
	}
	var messages []json.RawMessage
	if errMessages := json.Unmarshal(messagesRaw, &messages); errMessages != nil || messages == nil {
		return nil, OutcomeInvalidResponse
	}
	return messages, OutcomeApplied
}

type circuitBreaker struct {
	mu                  sync.Mutex
	consecutiveFailures int
	failures            []time.Time
	openUntil           time.Time
	halfOpenInFlight    bool
}

func (breaker *circuitBreaker) begin(now time.Time) (bool, bool) {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	if breaker.openUntil.IsZero() {
		return false, true
	}
	if now.Before(breaker.openUntil) || breaker.halfOpenInFlight {
		return false, false
	}
	breaker.halfOpenInFlight = true
	return true, true
}

func (breaker *circuitBreaker) cancel(probe bool) {
	if !probe {
		return
	}
	breaker.mu.Lock()
	breaker.halfOpenInFlight = false
	breaker.mu.Unlock()
}

func (breaker *circuitBreaker) finish(now time.Time, probe, success bool) {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	if probe {
		breaker.halfOpenInFlight = false
		if success {
			breaker.openUntil = time.Time{}
			breaker.consecutiveFailures = 0
			breaker.failures = nil
			return
		}
		breaker.openUntil = now.Add(circuitOpenDuration)
		return
	}
	if success {
		breaker.consecutiveFailures = 0
		return
	}
	cutoff := now.Add(-failureWindow)
	kept := breaker.failures[:0]
	for _, failure := range breaker.failures {
		if !failure.Before(cutoff) {
			kept = append(kept, failure)
		}
	}
	breaker.failures = append(kept, now)
	breaker.consecutiveFailures++
	if breaker.consecutiveFailures >= 3 || len(breaker.failures) >= 5 {
		breaker.openUntil = now.Add(circuitOpenDuration)
		breaker.halfOpenInFlight = false
	}
}
