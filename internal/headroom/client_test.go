package headroom

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

var validCompressRequest = []byte(`{"messages":[{"role":"user","content":"hello"}],"model":"test-model"}`)

func TestNewClientRejectsUnsafeEndpointsAndTimeouts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		url     string
		timeout time.Duration
	}{
		{name: "hostname", url: "http://localhost:8787", timeout: time.Second},
		{name: "external ipv4", url: "http://192.0.2.1:8787", timeout: time.Second},
		{name: "external ipv6", url: "http://[2001:db8::1]:8787", timeout: time.Second},
		{name: "https", url: "https://127.0.0.1:8787", timeout: time.Second},
		{name: "userinfo", url: "http://user:pass@127.0.0.1:8787", timeout: time.Second},
		{name: "configured path", url: "http://127.0.0.1:8787/v1/compress", timeout: time.Second},
		{name: "query", url: "http://127.0.0.1:8787?next=evil", timeout: time.Second},
		{name: "fragment", url: "http://127.0.0.1:8787#frag", timeout: time.Second},
		{name: "zero port", url: "http://127.0.0.1:0", timeout: time.Second},
		{name: "too short", url: "http://127.0.0.1:8787", timeout: 99 * time.Millisecond},
		{name: "too long", url: "http://127.0.0.1:8787", timeout: 1501 * time.Millisecond},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewClient(test.url, test.timeout); err == nil {
				t.Fatalf("NewClient(%q, %s) succeeded", test.url, test.timeout)
			}
		})
	}

	for _, rawURL := range []string{"http://127.0.0.1:8787", "http://[::1]:8787"} {
		client, err := NewClient(rawURL, 100*time.Millisecond)
		if err != nil {
			t.Fatalf("NewClient(%q): %v", rawURL, err)
		}
		if got := client.endpoint.String(); got != rawURL+"/v1/compress" {
			t.Fatalf("endpoint = %q", got)
		}
		client.CloseIdleConnections()
	}
}

func TestClientUsesFixedRequestContractWithoutCredentialsOrCompression(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.URL.Path != "/v1/compress" || request.URL.RawQuery != "" {
			t.Errorf("request target = %q", request.URL.RequestURI())
		}
		if request.Method != http.MethodPost {
			t.Errorf("method = %q", request.Method)
		}
		if got := request.Header.Get("Accept-Encoding"); got != "identity" {
			t.Errorf("Accept-Encoding = %q", got)
		}
		if got := request.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		for name := range request.Header {
			if name == "Authorization" || name == "Cookie" || name == "X-Api-Key" || name == "Proxy-Authorization" {
				t.Errorf("credential header %q was forwarded", name)
			}
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"messages":[{"role":"user","content":"short"}]}`)
	}))
	defer server.Close()

	client := mustClient(t, server.URL, time.Second)
	defer client.CloseIdleConnections()
	outcome := client.Compress(context.Background(), validCompressRequest, acceptAnyMessages)
	if outcome != OutcomeApplied {
		t.Fatalf("outcome = %q", outcome)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestClientRejectsRedirectAndIgnoresEnvironmentProxy(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	proxyHits := atomic.Int32{}
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	client := mustClient(t, redirector.URL, time.Second)
	defer client.CloseIdleConnections()
	if got := client.Compress(context.Background(), validCompressRequest, acceptAnyMessages); got != OutcomeRedirect {
		t.Fatalf("outcome = %q", got)
	}
	if redirected.Load() != 0 {
		t.Fatalf("redirect target received %d requests", redirected.Load())
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("environment proxy received %d requests", proxyHits.Load())
	}
}

func TestClientRejectsNonLoopbackConnectedPeer(t *testing.T) {
	t.Parallel()

	for _, address := range []string{"192.0.2.10:8787", "[2001:db8::10]:8787"} {
		t.Run(address, func(t *testing.T) {
			left, right := net.Pipe()
			defer right.Close()
			client, err := newClient("http://127.0.0.1:8787", time.Second, withDialContext(func(context.Context, string, string) (net.Conn, error) {
				return &remoteAddrConn{Conn: left, remote: stringAddr(address)}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			if got := client.Compress(context.Background(), validCompressRequest, acceptAnyMessages); got != OutcomeConnection {
				t.Fatalf("outcome = %q", got)
			}
		})
	}
}

func TestClientFailureOutcomesFailOpen(t *testing.T) {
	t.Parallel()

	large := make([]byte, maxPayloadBytes+1)
	tests := []struct {
		name      string
		roundTrip http.RoundTripper
		request   []byte
		validator func([]json.RawMessage) error
		want      Outcome
	}{
		{name: "oversized request", request: large, roundTrip: roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("network called"); return nil, nil }), validator: acceptAnyMessages, want: OutcomeRequestTooLarge},
		{name: "timeout", request: validCompressRequest, roundTrip: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}), validator: acceptAnyMessages, want: OutcomeTimeout},
		{name: "connection refused", request: validCompressRequest, roundTrip: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		}), validator: acceptAnyMessages, want: OutcomeConnection},
		{name: "non 2xx", request: validCompressRequest, roundTrip: staticResponse(http.StatusServiceUnavailable, "", `{}`), validator: acceptAnyMessages, want: OutcomeHTTPStatus},
		{name: "gzip", request: validCompressRequest, roundTrip: staticResponse(http.StatusOK, "gzip", `not really gzip`), validator: acceptAnyMessages, want: OutcomeResponseEncoding},
		{name: "br", request: validCompressRequest, roundTrip: staticResponse(http.StatusOK, "br", `{}`), validator: acceptAnyMessages, want: OutcomeResponseEncoding},
		{name: "invalid json", request: validCompressRequest, roundTrip: staticResponse(http.StatusOK, "", `{broken`), validator: acceptAnyMessages, want: OutcomeInvalidJSON},
		{name: "valid non-object json", request: validCompressRequest, roundTrip: staticResponse(http.StatusOK, "", `[]`), validator: acceptAnyMessages, want: OutcomeInvalidResponse},
		{name: "duplicate messages", request: validCompressRequest, roundTrip: staticResponse(http.StatusOK, "", `{"messages":[],"messages":[]}`), validator: acceptAnyMessages, want: OutcomeInvalidResponse},
		{name: "missing messages", request: validCompressRequest, roundTrip: staticResponse(http.StatusOK, "", `{"tokens_saved":1}`), validator: acceptAnyMessages, want: OutcomeInvalidResponse},
		{name: "validator rejected", request: validCompressRequest, roundTrip: staticResponse(http.StatusOK, "identity", `{"messages":[]}`), validator: func([]json.RawMessage) error { return errors.New("role changed") }, want: OutcomeInvalidResponse},
		{name: "oversized response", request: validCompressRequest, roundTrip: staticResponse(http.StatusOK, "", string(make([]byte, maxPayloadBytes+1))), validator: acceptAnyMessages, want: OutcomeResponseTooLarge},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := mustClient(t, "http://127.0.0.1:8787", 100*time.Millisecond)
			client.httpClient.Transport = test.roundTrip
			defer client.CloseIdleConnections()
			if got := client.Compress(context.Background(), test.request, test.validator); got != test.want {
				t.Fatalf("outcome = %q, want %q", got, test.want)
			}
		})
	}
}

func TestClientSaturationIsImmediateAndDoesNotQueue(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	var signalStarted sync.Once
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		signalStarted.Do(func() { close(started) })
		<-release
		return response(http.StatusOK, "", `{"messages":[]}`), nil
	})
	client := mustClient(t, "http://127.0.0.1:8787", time.Second)
	client.httpClient.Transport = transport
	defer client.CloseIdleConnections()

	firstDone := make(chan Outcome, 1)
	go func() {
		firstDone <- client.Compress(context.Background(), validCompressRequest, acceptAnyMessages)
	}()
	<-started
	if got := client.Compress(context.Background(), validCompressRequest, acceptAnyMessages); got != OutcomeSaturated {
		t.Fatalf("second outcome = %q", got)
	}
	close(release)
	if got := <-firstDone; got != OutcomeApplied {
		t.Fatalf("first outcome = %q", got)
	}
	if got := client.Compress(context.Background(), validCompressRequest, acceptAnyMessages); got != OutcomeApplied {
		t.Fatalf("post-release outcome = %q", got)
	}
}

func TestClientCircuitOpenAndSingleHalfOpenProbe(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Unix(1000, 0)}
	var mode atomic.Int32
	probeStarted := make(chan struct{})
	probeRelease := make(chan struct{})
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		switch mode.Load() {
		case 0:
			return response(http.StatusServiceUnavailable, "", `{}`), nil
		case 1:
			close(probeStarted)
			<-probeRelease
			return response(http.StatusOK, "", `{"messages":[]}`), nil
		default:
			return response(http.StatusOK, "", `{"messages":[]}`), nil
		}
	})
	client, err := newClient("http://127.0.0.1:8787", time.Second, withNow(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	client.httpClient.Transport = transport
	defer client.CloseIdleConnections()

	for i := 0; i < 3; i++ {
		if got := client.Compress(context.Background(), validCompressRequest, acceptAnyMessages); got != OutcomeHTTPStatus {
			t.Fatalf("failure %d outcome = %q", i+1, got)
		}
	}
	if got := client.Compress(context.Background(), validCompressRequest, acceptAnyMessages); got != OutcomeCircuitOpen {
		t.Fatalf("open outcome = %q", got)
	}

	clock.Advance(circuitOpenDuration)
	mode.Store(1)
	probeDone := make(chan Outcome, 1)
	go func() {
		probeDone <- client.Compress(context.Background(), validCompressRequest, acceptAnyMessages)
	}()
	<-probeStarted
	if got := client.Compress(context.Background(), validCompressRequest, acceptAnyMessages); got != OutcomeCircuitOpen {
		t.Fatalf("parallel half-open outcome = %q", got)
	}
	close(probeRelease)
	if got := <-probeDone; got != OutcomeApplied {
		t.Fatalf("probe outcome = %q", got)
	}

	mode.Store(2)
	if got := client.Compress(context.Background(), validCompressRequest, acceptAnyMessages); got != OutcomeApplied {
		t.Fatalf("closed outcome = %q", got)
	}
}

func TestClientFiveFailuresWithinWindowOpenCircuit(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Unix(2000, 0)}
	var calls atomic.Int32
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		call := calls.Add(1)
		if call%2 == 0 {
			return response(http.StatusOK, "", `{"messages":[]}`), nil
		}
		return response(http.StatusInternalServerError, "", `{}`), nil
	})
	client, err := newClient("http://127.0.0.1:8787", time.Second, withNow(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	client.httpClient.Transport = transport
	defer client.CloseIdleConnections()

	for failures := 0; failures < 5; {
		got := client.Compress(context.Background(), validCompressRequest, acceptAnyMessages)
		if got == OutcomeHTTPStatus {
			failures++
		} else if got != OutcomeApplied {
			t.Fatalf("unexpected outcome before threshold = %q", got)
		}
		clock.Advance(5 * time.Second)
	}
	if got := client.Compress(context.Background(), validCompressRequest, acceptAnyMessages); got != OutcomeCircuitOpen {
		t.Fatalf("outcome = %q", got)
	}
}

func TestClientReusesConnectionAndReleasesInFlightSlot(t *testing.T) {
	t.Parallel()

	var newConnections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"messages":[]}`)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConnections.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	client := mustClient(t, server.URL, time.Second)
	for i := 0; i < 2; i++ {
		if got := client.Compress(context.Background(), validCompressRequest, acceptAnyMessages); got != OutcomeApplied {
			t.Fatalf("call %d outcome = %q", i+1, got)
		}
	}
	if got := newConnections.Load(); got != 1 {
		t.Fatalf("new connections = %d, want 1", got)
	}
	if got := len(client.semaphore); got != 0 {
		t.Fatalf("in-flight slots = %d", got)
	}
	client.CloseIdleConnections()
}

func TestClientProbeUsesShortCacheWithoutRetry(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Unix(3000, 0)}
	var calls atomic.Int32
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(http.StatusOK, "", `{"messages":[{"role":"user","content":"ping"}]}`), nil
	})
	client, err := newClient("http://127.0.0.1:8787", time.Second, withNow(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	client.httpClient.Transport = transport
	defer client.CloseIdleConnections()

	if got := client.Probe(context.Background()); got != OutcomeApplied {
		t.Fatalf("first probe = %q", got)
	}
	if got := client.Probe(context.Background()); got != OutcomeApplied {
		t.Fatalf("cached probe = %q", got)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls within cache window = %d", got)
	}
	clock.Advance(probeCacheDuration)
	if got := client.Probe(context.Background()); got != OutcomeApplied {
		t.Fatalf("refreshed probe = %q", got)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls after cache expiry = %d", got)
	}
}

func mustClient(t *testing.T, rawURL string, timeout time.Duration) *Client {
	t.Helper()
	client, err := NewClient(rawURL, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func acceptAnyMessages([]json.RawMessage) error { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func staticResponse(status int, encoding, body string) http.RoundTripper {
	return roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(status, encoding, body), nil
	})
}

func response(status int, encoding, body string) *http.Response {
	header := make(http.Header)
	if encoding != "" {
		header.Set("Content-Encoding", encoding)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(&stringReader{value: body}),
	}
}

type stringReader struct {
	value  string
	offset int
}

func (reader *stringReader) Read(buffer []byte) (int, error) {
	if reader.offset >= len(reader.value) {
		return 0, io.EOF
	}
	count := copy(buffer, reader.value[reader.offset:])
	reader.offset += count
	return count, nil
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

type remoteAddrConn struct {
	net.Conn
	remote net.Addr
}

func (connection *remoteAddrConn) RemoteAddr() net.Addr { return connection.remote }

type stringAddr string

func (address stringAddr) Network() string { return "tcp" }
func (address stringAddr) String() string  { return string(address) }
