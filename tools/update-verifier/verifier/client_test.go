package verifier

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pluginconfig "github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/config"
)

func TestVerifyUsesInProcessAuthorizationAndChecksStableRuntime(t *testing.T) {
	credential := "MANAGEMENT_SENTINEL_DO_NOT_LEAK"
	configBody := []byte(`{"enabled":true,"priority":-100,"rtk_enabled":false,"headroom_enabled":false,"headroom_url":"http://127.0.0.1:8787","headroom_timeout_ms":1500,"caveman_enabled":false,"caveman_level":"full","ponytail_enabled":false,"ponytail_level":"full","model_allowlist":[]}`)
	parsed, errParse := pluginconfig.Parse(configBody)
	if errParse != nil {
		t.Fatal(errParse)
	}
	digest := pluginconfig.Digest(parsed)
	approval, options := verifierFixture(t)
	status := Status{
		BuildVersion: approval.Plugin.Version, ABIVersion: 1, RPCSchema: 3, FixtureRevision: "v1",
		Live: true, Config: "valid", ConfigGeneration: 9, ConfigDigest: digest,
		Pipeline: "all_bypassed", Dependency: "disabled",
	}
	var statusCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+credential {
			t.Errorf("Authorization = %q", got)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/plugins":
			_, _ = w.Write([]byte(`{"plugins_enabled":true,"plugins":[{"id":"token-saver","registered":true,"effective_enabled":true,"metadata":{"version":"` + approval.Plugin.Version + `"}}]}`))
		case "/v0/management/plugins/token-saver/config":
			_, _ = w.Write(configBody)
		case "/v0/management/plugins/token-saver/status":
			statusCalls.Add(1)
			_ = json.NewEncoder(w).Encode(status)
		case "/v0/management/plugins/token-saver/self-test":
			_ = json.NewEncoder(w).Encode(SelfTest{FixtureRevision: "v1", Result: "passed"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	options.BaseURL = server.URL
	options.Credential = credential

	result := verify(t.Context(), options, approval.CLI.Arch, timings{core: time.Second, request: time.Second, poll: 5 * time.Millisecond})
	if !result.Compatible || result.Code != CodeOK || statusCalls.Load() != 2 {
		t.Fatalf("verify() = %#v, status calls = %d", result, statusCalls.Load())
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), credential) {
		t.Fatalf("result leaked credential: %s", raw)
	}
}

func TestVerifyClassifiesAuthenticationFailureAsBlocked(t *testing.T) {
	approval, options := verifierFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	options.BaseURL = server.URL
	options.Credential = "wrong"

	result := verify(t.Context(), options, approval.CLI.Arch, timings{core: time.Second, request: time.Second, poll: 5 * time.Millisecond})
	if result.Compatible || result.Classification != ClassificationBlocked || result.Code != CodeManagementAuth {
		t.Fatalf("verify() = %#v", result)
	}
}

func TestManagementBaseURLMustBeLiteralLoopback(t *testing.T) {
	for _, value := range []string{
		"https://127.0.0.1:8317",
		"http://localhost:8317",
		"http://127.0.0.2:8317",
		"http://user@127.0.0.1:8317",
		"http://127.0.0.1:8317/path",
	} {
		if _, ok := managementBaseURL(value); ok {
			t.Errorf("managementBaseURL(%q) unexpectedly accepted", value)
		}
	}
	for _, value := range []string{"http://127.0.0.1:8317", "http://[::1]:8317"} {
		if _, ok := managementBaseURL(value); !ok {
			t.Errorf("managementBaseURL(%q) rejected literal loopback", value)
		}
	}
}

func verifierFixture(t *testing.T) (Approval, Options) {
	t.Helper()
	directory := t.TempDir()
	paths := []string{
		filepath.Join(directory, "cli-proxy-api"),
		filepath.Join(directory, "token-saver.so"),
	}
	contents := [][]byte{[]byte("cli"), []byte("plugin")}
	for index := range paths {
		if errWrite := os.WriteFile(paths[index], contents[index], 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	approval := validApproval()
	approval.CLI.SHA256, _ = hashFile(paths[0])
	approval.Plugin.SHA256, _ = hashFile(paths[1])
	return approval, Options{
		Phase: PhasePostInstall, CLIPath: paths[0], PluginPath: paths[1],
		Approval: approval,
	}
}
