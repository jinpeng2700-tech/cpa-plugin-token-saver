package verifier

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	pluginconfig "github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/config"
)

const maxManagementBody = 1 << 20

type timings struct {
	core    time.Duration
	request time.Duration
	poll    time.Duration
}

type requestResult uint8

const (
	requestOK requestResult = iota
	requestAuthFailure
	requestFailure
)

type pluginListResponse struct {
	Plugins []struct {
		ID               string `json:"id"`
		Registered       bool   `json:"registered"`
		EffectiveEnabled bool   `json:"effective_enabled"`
		Metadata         *struct {
			Version string `json:"version"`
		} `json:"metadata"`
	} `json:"plugins"`
}

func Verify(ctx context.Context, options Options) Result {
	return verify(ctx, options, runtime.GOOS+"-"+runtime.GOARCH, timings{
		core:    30 * time.Second,
		request: 10 * time.Second,
		poll:    100 * time.Millisecond,
	})
}

func verify(ctx context.Context, options Options, architecture string, limits timings) Result {
	if options.Phase != PhasePreflight && options.Phase != PhasePostInstall {
		return blocked(CodeApprovalInvalid)
	}
	baseURL, okBaseURL := managementBaseURL(options.BaseURL)
	if !okBaseURL {
		return blocked(CodeManagementURL)
	}
	cliHash, okCLI := hashFile(options.CLIPath)
	pluginHash, okPlugin := hashFile(options.PluginPath)
	if !okCLI || !okPlugin {
		return blocked(CodeArtifactRead)
	}
	if result := VerifyArtifacts(options.Phase, options.Approval, Artifacts{
		Arch: architecture, CLIHash: cliHash, PluginHash: pluginHash,
	}); !result.Compatible {
		return result
	}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: limits.request}).DialContext,
			TLSHandshakeTimeout:   limits.request,
			ResponseHeaderTimeout: limits.request,
			DisableCompression:    true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if !waitForCore(ctx, client, baseURL, limits) {
		return phaseFailure(options.Phase, CodeCoreUnavailable)
	}

	var plugins pluginListResponse
	if outcome := getJSON(ctx, client, baseURL+"/v0/management/plugins", options.Credential, limits.request, &plugins); outcome != requestOK {
		return requestFailureResult(options.Phase, outcome, CodeManagementUnavailable)
	}
	plugin := PluginState{}
	for _, entry := range plugins.Plugins {
		if entry.ID != "token-saver" {
			continue
		}
		plugin.Found = true
		plugin.Registered = entry.Registered
		plugin.EffectiveEnabled = entry.EffectiveEnabled
		if entry.Metadata != nil {
			plugin.Version = entry.Metadata.Version
		}
		break
	}
	if !plugin.Found {
		return phaseFailure(options.Phase, CodePluginMissing)
	}
	if !plugin.Registered {
		return phaseFailure(options.Phase, CodePluginNotRegistered)
	}
	if !plugin.EffectiveEnabled {
		return phaseFailure(options.Phase, CodePluginNotEffective)
	}

	var before Status
	statusURL := baseURL + "/v0/management/plugins/token-saver/status"
	if outcome := getJSON(ctx, client, statusURL, options.Credential, limits.request, &before); outcome != requestOK {
		return requestFailureResult(options.Phase, outcome, CodeRuntimeUnhealthy)
	}
	configRaw, outcomeConfig := getRaw(ctx, client, baseURL+"/v0/management/plugins/token-saver/config", options.Credential, limits.request)
	if outcomeConfig != requestOK {
		return requestFailureResult(options.Phase, outcomeConfig, CodeManagementUnavailable)
	}
	parsedConfig, errConfig := pluginconfig.Parse(configRaw)
	if errConfig != nil {
		return phaseFailure(options.Phase, CodeConfigInvalid)
	}

	var selfTest SelfTest
	if outcome := postJSON(ctx, client, baseURL+"/v0/management/plugins/token-saver/self-test", options.Credential, limits.request, &selfTest); outcome != requestOK {
		return requestFailureResult(options.Phase, outcome, CodeSelfTestFailed)
	}
	var after Status
	if outcome := getJSON(ctx, client, statusURL, options.Credential, limits.request, &after); outcome != requestOK {
		return requestFailureResult(options.Phase, outcome, CodeRuntimeUnhealthy)
	}
	return EvaluateRuntime(options.Phase, options.Approval, RuntimeObservation{
		Plugin: plugin, Before: before, After: after,
		ConfigDigest: pluginconfig.Digest(parsedConfig), SelfTest: selfTest,
	})
}

func waitForCore(parent context.Context, client *http.Client, baseURL string, limits timings) bool {
	ctx, cancel := context.WithTimeout(parent, limits.core)
	defer cancel()
	ticker := time.NewTicker(limits.poll)
	defer ticker.Stop()
	for {
		req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/healthz", nil)
		if errRequest == nil {
			response, errDo := client.Do(req)
			if errDo == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return true
				}
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

func getJSON(ctx context.Context, client *http.Client, endpoint, credential string, timeout time.Duration, target any) requestResult {
	raw, outcome := request(ctx, client, http.MethodGet, endpoint, credential, timeout, nil)
	if outcome != requestOK {
		return outcome
	}
	if errDecode := json.Unmarshal(raw, target); errDecode != nil {
		return requestFailure
	}
	return requestOK
}

func postJSON(ctx context.Context, client *http.Client, endpoint, credential string, timeout time.Duration, target any) requestResult {
	raw, outcome := request(ctx, client, http.MethodPost, endpoint, credential, timeout, []byte(`{}`))
	if outcome != requestOK {
		return outcome
	}
	if errDecode := json.Unmarshal(raw, target); errDecode != nil {
		return requestFailure
	}
	return requestOK
}

func getRaw(ctx context.Context, client *http.Client, endpoint, credential string, timeout time.Duration) ([]byte, requestResult) {
	return request(ctx, client, http.MethodGet, endpoint, credential, timeout, nil)
}

func request(parent context.Context, client *http.Client, method, endpoint, credential string, timeout time.Duration, body []byte) ([]byte, requestResult) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	req, errRequest := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if errRequest != nil {
		return nil, requestFailure
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, errDo := client.Do(req)
	if errDo != nil {
		return nil, requestFailure
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxManagementBody+1))
		return nil, requestAuthFailure
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxManagementBody+1))
		return nil, requestFailure
	}
	raw, errRead := io.ReadAll(io.LimitReader(response.Body, maxManagementBody+1))
	if errRead != nil || len(raw) > maxManagementBody {
		return nil, requestFailure
	}
	return raw, requestOK
}

func requestFailureResult(phase Phase, outcome requestResult, code string) Result {
	if outcome == requestAuthFailure {
		return blocked(CodeManagementAuth)
	}
	return phaseFailure(phase, code)
}

func managementBaseURL(value string) (string, bool) {
	parsed, errParse := url.Parse(strings.TrimSpace(value))
	if errParse != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.Port() == "" {
		return "", false
	}
	host := parsed.Hostname()
	if host != "127.0.0.1" && host != "::1" {
		return "", false
	}
	parsed.Path = ""
	return strings.TrimSuffix(parsed.String(), "/"), true
}

func hashFile(path string) (string, bool) {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return "", false
	}
	defer file.Close()
	info, errStat := file.Stat()
	if errStat != nil || !info.Mode().IsRegular() {
		return "", false
	}
	hash := sha256.New()
	if _, errCopy := io.Copy(hash, file); errCopy != nil {
		return "", false
	}
	return hex.EncodeToString(hash.Sum(nil)), true
}
