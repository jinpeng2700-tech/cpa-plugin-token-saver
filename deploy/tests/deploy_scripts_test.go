package deploytests

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func readRepositoryFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

func TestDeploymentArtifactsExist(t *testing.T) {
	for _, name := range []string{
		"deploy/update-wrapper.sh",
		"deploy/approved-artifacts.example.json",
		"deploy/security-overrides.example.json",
		"deploy/systemd/cliproxyapi-updater.service.d/credentials.conf",
	} {
		t.Run(name, func(t *testing.T) {
			info, err := os.Stat(filepath.Join(repositoryRoot(t), filepath.FromSlash(name)))
			if err != nil {
				t.Fatalf("stat %s: %v", name, err)
			}
			if !info.Mode().IsRegular() {
				t.Fatalf("%s is not a regular file", name)
			}
		})
	}
}

func TestCredentialDropInUsesOnlySystemdCredentialChannel(t *testing.T) {
	content := readRepositoryFile(t, "deploy/systemd/cliproxyapi-updater.service.d/credentials.conf")
	for _, want := range []string{
		"LoadCredential=cliproxyapi-management-key:/root/",
		"ExecStart=/root/cliproxyapi/update-wrapper.sh latest",
		"Requires systemd >= 247",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("drop-in missing %q", want)
		}
	}
	if strings.Contains(content, "Environment=CLIPROXYAPI_MANAGEMENT_KEY") {
		t.Fatal("management credential must not be injected as an environment variable")
	}
}

func TestWrapperContractDoesNotCallManagementAPI(t *testing.T) {
	content := readRepositoryFile(t, "deploy/update-wrapper.sh")
	for _, want := range []string{"compat-probe", "update-verifier", "update.sh", "CREDENTIALS_DIRECTORY", "247"} {
		if !strings.Contains(content, want) {
			t.Errorf("wrapper missing %q", want)
		}
	}
	if strings.Contains(content, "/v0/management") {
		t.Fatal("wrapper must not call the Management API")
	}
}

func TestWrapperDefaultPluginPathMatchesHostLayout(t *testing.T) {
	content := readRepositoryFile(t, "deploy/update-wrapper.sh")
	if !strings.Contains(content, "/root/.cli-proxy-api/plugins/linux/$archive_arch/token-saver.so") {
		t.Fatal("default plugin path must use the host's plugins/linux/<arch> directory")
	}
}
