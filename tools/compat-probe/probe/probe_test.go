package probe

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParsePluginFilenameMatchesHostRules(t *testing.T) {
	tests := []struct {
		name        string
		filename    string
		wantID      string
		wantVersion string
		wantOK      bool
	}{
		{name: "versioned release", filename: "token-saver-v1.2.3-linux-amd64.so", wantID: "token-saver", wantVersion: "1.2.3-linux-amd64", wantOK: true},
		{name: "unversioned host file", filename: "token-saver.so", wantID: "token-saver", wantOK: true},
		{name: "wrong plugin id", filename: "cpa-plugin-token-saver-v1.2.3.so", wantID: "cpa-plugin-token-saver", wantVersion: "1.2.3", wantOK: true},
		{name: "leading v is not a host version", filename: "token-saver-vv1.2.3.so", wantID: "token-saver-vv1.2.3", wantOK: true},
		{name: "wrong extension", filename: "token-saver-v1.2.3.dll", wantOK: false},
		{name: "invalid id", filename: "token saver-v1.2.3.so", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, version, ok := parsePluginFilename(tt.filename)
			if id != tt.wantID || version != tt.wantVersion || ok != tt.wantOK {
				t.Fatalf("parsePluginFilename(%q) = %q, %q, %v; want %q, %q, %v", tt.filename, id, version, ok, tt.wantID, tt.wantVersion, tt.wantOK)
			}
		})
	}
}

func TestMarkerCountRequiresExactlyOneStableMarker(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want int
	}{
		{name: "absent", body: `{"messages":[{"role":"user","content":"hello"}]}`, want: 0},
		{name: "once", body: `{"messages":[{"role":"system","content":"[CPA_TOKEN_SAVER_CAVEMAN_START]"}]}`, want: 1},
		{name: "duplicated", body: `[CPA_TOKEN_SAVER_CAVEMAN_START]\n[CPA_TOKEN_SAVER_CAVEMAN_START]`, want: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := markerCount([]byte(tt.body)); got != tt.want {
				t.Fatalf("markerCount() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRequiredScenariosCoverFullPipeline(t *testing.T) {
	want := "all-off,rtk,headroom-rewrite,headroom-timeout,caveman,ponytail,fixed-order"
	if got := strings.Join(requiredScenarios, ","); got != want {
		t.Fatalf("requiredScenarios = %q, want %q", got, want)
	}
}

func TestPublicStatusValidationRequiresExactSafeFields(t *testing.T) {
	valid := map[string]json.RawMessage{
		"enabled": json.RawMessage(`false`),
		"status":  json.RawMessage(`"disabled"`),
		"circuit": json.RawMessage(`"disabled"`),
	}
	if !validPublicStatus(valid, false, "disabled", "disabled") {
		t.Fatal("valid public status was rejected")
	}
	valid["build_version"] = json.RawMessage(`"1.1.0"`)
	if validPublicStatus(valid, false, "disabled", "disabled") {
		t.Fatal("public status with extra fingerprint field was accepted")
	}
}

func TestDashboardValidationRequiresExactSafeFields(t *testing.T) {
	valid := []byte(`{
		"started_at": "2026-08-21T00:00:00Z",
		"headroom": {
			"enabled": true,
			"url": "http://127.0.0.1:8787",
			"status": "ready",
			"circuit": "closed",
			"last_checked_at": "2026-08-21T00:00:00Z",
			"last_latency_ms": 12,
			"last_outcome": "applied"
		},
		"stages": {
			"rtk": {"executed":1,"bypassed":0,"fail_open":0,"timeout":0,"saturated":0,"input_bytes":100,"output_bytes":60,"saved_bytes":40,"duration_ns":1000},
			"headroom": {"executed":0,"bypassed":0,"fail_open":0,"timeout":0,"saturated":0,"input_bytes":0,"output_bytes":0,"saved_bytes":0,"duration_ns":0},
			"caveman": {"executed":0,"bypassed":0,"fail_open":0,"timeout":0,"saturated":0,"input_bytes":0,"output_bytes":0,"saved_bytes":0,"duration_ns":0},
			"ponytail": {"executed":0,"bypassed":0,"fail_open":0,"timeout":0,"saturated":0,"input_bytes":0,"output_bytes":0,"saved_bytes":0,"duration_ns":0}
		}
	}`)
	dash, ok := validDashboardRaw(valid, "http://127.0.0.1:8787")
	if !ok || dash == nil {
		t.Fatal("valid dashboard was rejected")
	}

	unknownField := []byte(`{"started_at":"2026-08-21T00:00:00Z","extra":1,"headroom":{"enabled":true,"url":"http://127.0.0.1:8787","status":"ready","circuit":"closed","last_checked_at":null,"last_latency_ms":null,"last_outcome":"unknown"},"stages":{"rtk":{"executed":0,"bypassed":0,"fail_open":0,"timeout":0,"saturated":0,"input_bytes":0,"output_bytes":0,"saved_bytes":0,"duration_ns":0},"headroom":{"executed":0,"bypassed":0,"fail_open":0,"timeout":0,"saturated":0,"input_bytes":0,"output_bytes":0,"saved_bytes":0,"duration_ns":0},"caveman":{"executed":0,"bypassed":0,"fail_open":0,"timeout":0,"saturated":0,"input_bytes":0,"output_bytes":0,"saved_bytes":0,"duration_ns":0},"ponytail":{"executed":0,"bypassed":0,"fail_open":0,"timeout":0,"saturated":0,"input_bytes":0,"output_bytes":0,"saved_bytes":0,"duration_ns":0}}}`)
	if _, okUnknown := validDashboardRaw(unknownField, "http://127.0.0.1:8787"); okUnknown {
		t.Fatal("dashboard with unknown top-level field was accepted")
	}

	missingField := []byte(`{"started_at":"2026-08-21T00:00:00Z","headroom":{"enabled":true,"url":"http://127.0.0.1:8787","status":"ready","circuit":"closed","last_checked_at":null,"last_latency_ms":null,"last_outcome":"unknown"}}`)
	if _, okMissing := validDashboardRaw(missingField, "http://127.0.0.1:8787"); okMissing {
		t.Fatal("dashboard missing stages was accepted")
	}

	savedMismatch := []byte(`{
		"started_at": "2026-08-21T00:00:00Z",
		"headroom": {"enabled":true,"url":"http://127.0.0.1:8787","status":"ready","circuit":"closed","last_checked_at":null,"last_latency_ms":null,"last_outcome":"unknown"},
		"stages": {
			"rtk": {"executed":1,"bypassed":0,"fail_open":0,"timeout":0,"saturated":0,"input_bytes":100,"output_bytes":60,"saved_bytes":10,"duration_ns":1000},
			"headroom": {"executed":0,"bypassed":0,"fail_open":0,"timeout":0,"saturated":0,"input_bytes":0,"output_bytes":0,"saved_bytes":0,"duration_ns":0},
			"caveman": {"executed":0,"bypassed":0,"fail_open":0,"timeout":0,"saturated":0,"input_bytes":0,"output_bytes":0,"saved_bytes":0,"duration_ns":0},
			"ponytail": {"executed":0,"bypassed":0,"fail_open":0,"timeout":0,"saturated":0,"input_bytes":0,"output_bytes":0,"saved_bytes":0,"duration_ns":0}
		}
	}`)
	if _, okSaved := validDashboardRaw(savedMismatch, "http://127.0.0.1:8787"); okSaved {
		t.Fatal("dashboard with saved_bytes mismatch was accepted")
	}
}

func TestHeadroomCheckValidationRequiresExactSafeFields(t *testing.T) {
	valid := []byte(`{"reachable":true,"status":"ready","outcome":"applied","latency_ms":12,"tested_at":"2026-08-21T00:00:00Z"}`)
	check, ok := validHeadroomCheckRaw(valid)
	if !ok || check == nil || !check.Reachable || check.LatencyMS != 12 {
		t.Fatal("valid headroom check was rejected")
	}

	unknownField := []byte(`{"reachable":true,"status":"ready","outcome":"applied","latency_ms":12,"tested_at":"2026-08-21T00:00:00Z","extra":"leak"}`)
	if _, okUnknown := validHeadroomCheckRaw(unknownField); okUnknown {
		t.Fatal("headroom check with unknown field was accepted")
	}

	missingField := []byte(`{"reachable":true,"status":"ready","outcome":"applied","latency_ms":12}`)
	if _, okMissing := validHeadroomCheckRaw(missingField); okMissing {
		t.Fatal("headroom check missing tested_at was accepted")
	}
}

func TestMockCapturesStayBounded(t *testing.T) {
	provider := &mockProvider{}
	for _, body := range []string{
		`{"model":"compat-model","messages":[{"role":"user","content":"first"}]}`,
		`{"model":"compat-model","messages":[{"role":"user","content":"second"}]}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
		request.Header.Set("Authorization", "Bearer compat-upstream-key-only")
		provider.handleChat(httptest.NewRecorder(), request)
	}
	if got := len(provider.bodies); got != 1 {
		t.Fatalf("provider retained %d request bodies, want 1", got)
	}

	headroom := &mockHeadroom{}
	for _, body := range []string{
		`{"model":"compat-model","messages":[{"role":"user","content":"dispatch"}]}`,
		`{"model":"headroom-health-probe","messages":[]}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/compress", bytes.NewBufferString(body))
		headroom.handleCompress(httptest.NewRecorder(), request)
	}
	if got := len(headroom.bodies); got != 1 {
		t.Fatalf("headroom retained %d request bodies, want 1", got)
	}
	if got := messageContent(headroom.LastDispatchBody(), "user"); got != "dispatch" {
		t.Fatalf("headroom last dispatch content = %q, want dispatch", got)
	}
}

func TestRunClassifiesCandidateExitWithoutLeakingPaths(t *testing.T) {
	candidate := "/bin/false"
	if runtime.GOOS == "windows" {
		var errLookPath error
		candidate, errLookPath = exec.LookPath("cmd.exe")
		if errLookPath != nil {
			t.Skip("cmd.exe is unavailable")
		}
	}
	tempDir := t.TempDir()
	pluginPath := filepath.Join(tempDir, "token-saver-v1.2.3-linux-amd64.so")
	if errWrite := os.WriteFile(pluginPath, []byte("not-a-real-plugin"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	report := Run(t.Context(), Options{CandidatePath: candidate, PluginPath: pluginPath, Timeout: 2 * time.Second})
	if report.Compatible || report.Code != CodeCandidateExit {
		t.Fatalf("Run() report = %#v, want candidate exit", report)
	}
	raw := report.JSON()
	if strings.Contains(string(raw), tempDir) || strings.Contains(string(raw), "not-a-real-plugin") {
		t.Fatalf("report leaked a path or artifact content: %s", raw)
	}
}

func TestRunAcceptsProductionStablePluginFilename(t *testing.T) {
	candidate := "/bin/false"
	if runtime.GOOS == "windows" {
		var errLookPath error
		candidate, errLookPath = exec.LookPath("cmd.exe")
		if errLookPath != nil {
			t.Skip("cmd.exe is unavailable")
		}
	}
	tempDir := t.TempDir()
	pluginPath := filepath.Join(tempDir, "token-saver.so")
	if errWrite := os.WriteFile(pluginPath, []byte("not-a-real-plugin"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	report := Run(t.Context(), Options{CandidatePath: candidate, PluginPath: pluginPath, Timeout: 2 * time.Second})
	if report.Compatible || report.Code != CodeCandidateExit {
		t.Fatalf("Run() with production stable plugin name = %#v, want candidate exit after identity validation", report)
	}
}

func TestSanitizedCandidateEnvironmentDropsSentinelAndUsesAllowlist(t *testing.T) {
	const sentinelName = "COMPAT_PROBE_SENTINEL_SECRET"
	const sentinelValue = "sentinel-must-never-reach-candidate"
	t.Setenv(sentinelName, sentinelValue)
	t.Setenv("GH_TOKEN", sentinelValue)
	t.Setenv("GITHUB_TOKEN", sentinelValue)

	allowed := map[string]struct{}{
		"COMSPEC": {}, "HOME": {}, "LANG": {}, "LC_ALL": {}, "NO_PROXY": {}, "PATH": {},
		"PATHEXT": {}, "SYSTEMROOT": {}, "TEMP": {}, "TMP": {}, "TMPDIR": {}, "TZ": {}, "WINDIR": {},
	}
	environment := sanitizedCandidateEnvironment()
	for _, item := range environment {
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			t.Fatalf("candidate environment entry has no assignment: %q", item)
		}
		if name == sentinelName || value == sentinelValue {
			t.Fatalf("candidate environment leaked sentinel through %q", name)
		}
	}
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		if _, okAllowed := allowed[strings.ToUpper(name)]; !okAllowed {
			t.Fatalf("candidate environment contains non-allowlisted variable %q", name)
		}
	}
}

func TestRunCoreOnlyClassifiesCandidateExitWithoutPluginArtifact(t *testing.T) {
	candidate := "/bin/false"
	if runtime.GOOS == "windows" {
		var errLookPath error
		candidate, errLookPath = exec.LookPath("cmd.exe")
		if errLookPath != nil {
			t.Skip("cmd.exe is unavailable")
		}
	}

	report := Run(t.Context(), Options{Mode: ModeCoreOnly, CandidatePath: candidate, Timeout: 2 * time.Second})
	if report.Compatible || report.Code != CodeCandidateExit {
		t.Fatalf("Run(core-only) report = %#v, want candidate exit", report)
	}
}
