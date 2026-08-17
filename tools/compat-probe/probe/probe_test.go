package probe

import (
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
