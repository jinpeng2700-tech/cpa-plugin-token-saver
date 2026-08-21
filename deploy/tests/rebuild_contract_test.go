package deploytests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/tools/update-verifier/verifier"
)

func TestRebuildArtifactsExist(t *testing.T) {
	for _, name := range []string{
		"deploy/rebuild/assemble-bundle.py",
		"deploy/rebuild/validate-bundle.py",
		"deploy/rebuild/stage-release.sh",
		"deploy/rebuild/activate-release.sh",
		"deploy/rebuild/rollback-release.sh",
		"deploy/rebuild/config/config.template.yaml",
		"deploy/rebuild/systemd/cliproxyapi.service",
		"deploy/rebuild/nginx/cpa.ai2c.asia.conf",
		"deploy/rebuild/firewall/cpa-network-guard.nft",
		"deploy/rebuild/firewall/cpa-network-guard.service",
		"deploy/rebuild/firewall/cpa-network-guard.env.template",
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

func TestRebuildSafeDefaultsAndAtomicSwitch(t *testing.T) {
	for _, name := range []string{
		"deploy/rebuild/stage-release.sh",
		"deploy/rebuild/activate-release.sh",
		"deploy/rebuild/rollback-release.sh",
	} {
		content := readRepositoryFile(t, name)
		for _, want := range []string{"--apply", "DRY RUN", "id -u", "root"} {
			if !strings.Contains(content, want) {
				t.Errorf("%s missing %q", name, want)
			}
		}
	}
	stage := readRepositoryFile(t, "deploy/rebuild/stage-release.sh")
	for _, want := range []string{
		"$temporary/deploy/stage-release.sh",
		"$temporary/deploy/activate-release.sh",
		"$temporary/deploy/rollback-release.sh",
		"$temporary/deploy/validate-bundle.py",
	} {
		if !strings.Contains(stage, want) {
			t.Errorf("stage script missing executable mode repair for %q", want)
		}
	}

	for _, name := range []string{
		"deploy/rebuild/activate-release.sh",
		"deploy/rebuild/rollback-release.sh",
	} {
		content := readRepositoryFile(t, name)
		for _, want := range []string{"ln -s", "mv -T", "cliproxyapi.prev"} {
			if !strings.Contains(content, want) {
				t.Errorf("%s missing atomic switch primitive %q", name, want)
			}
		}
	}
	if !strings.Contains(readRepositoryFile(t, "deploy/rebuild/rollback-release.sh"), "legacy-artifacts.json") {
		t.Fatal("rollback must accept the one-time legacy deployment marker")
	}
}

func TestRebuildConfigAndNetworkContracts(t *testing.T) {
	config := readRepositoryFile(t, "deploy/rebuild/config/config.template.yaml")
	for _, want := range []string{
		"host: 127.0.0.1",
		"port: 8317",
		"dir: /root/cliproxyapi/plugins",
		"allow-remote: true",
		"rtk_enabled: false",
		"headroom_enabled: false",
		"caveman_enabled: false",
		"ponytail_enabled: false",
		"model_allowlist:",
		"REPLACE_WITH_ONE_EXACT_MODEL_ID",
	} {
		if !strings.Contains(config, want) {
			t.Errorf("config template missing %q", want)
		}
	}

	nginx := readRepositoryFile(t, "deploy/rebuild/nginx/cpa.ai2c.asia.conf")
	for _, want := range []string{
		"return 301 https://$host$request_uri",
		"location ^~ /.well-known/acme-challenge/",
		"proxy_buffering off",
		"proxy_request_buffering off",
		"proxy_read_timeout",
		"proxy_set_header Upgrade $http_upgrade",
		"limit_req zone=management_api",
		"proxy_set_header X-Forwarded-For $remote_addr",
		"proxy_set_header Forwarded",
	} {
		if !strings.Contains(nginx, want) {
			t.Errorf("nginx template missing %q", want)
		}
	}

	nft := readRepositoryFile(t, "deploy/rebuild/firewall/cpa-network-guard.nft")
	if strings.Contains(nft, "flush ruleset") {
		t.Fatal("nft template must not flush unrelated Docker/Podman rules")
	}
	for _, want := range []string{
		"table inet cpa_network_guard",
		"iifname \"lo\"",
		"@podman_subnets",
		"8317",
		"8787",
		"18317",
		"1455",
		"54545",
		"51121",
		"reject",
	} {
		if !strings.Contains(nft, want) {
			t.Errorf("nft template missing %q", want)
		}
	}

	service := readRepositoryFile(t, "deploy/rebuild/firewall/cpa-network-guard.service")
	for _, want := range []string{"PODMAN_SUBNET", "delete table inet cpa_network_guard", "add element inet cpa_network_guard"} {
		if !strings.Contains(service, want) {
			t.Errorf("firewall service missing %q", want)
		}
	}
}

func TestRebuildBundleRoundTripAndSecretRejection(t *testing.T) {
	python := "python3"
	if runtime.GOOS == "windows" {
		python = "python"
	}
	if _, err := exec.LookPath(python); err != nil {
		t.Skipf("%s unavailable: %v", python, err)
	}

	root := repositoryRoot(t)
	input := t.TempDir()
	for name, body := range map[string]string{
		"cli-proxy-api":         "\x7fELF\x00sk-false-positive-binary-string\n",
		"token-saver-v1.0.2.so": "\x7fELF\x00fake plugin\n",
		"compat-probe":          "\x7fELF\x00fake probe\n",
		"update-verifier":       "\x7fELF\x00fake verifier\n",
	} {
		if err := os.WriteFile(filepath.Join(input, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	output := filepath.Join(t.TempDir(), "bundle")
	assemble := filepath.Join(root, "deploy", "rebuild", "assemble-bundle.py")
	args := []string{
		assemble,
		"--input-dir", input,
		"--output-dir", output,
		"--deployment-id", "test-7.2.136-1.0.2",
		"--plugin-source-commit", "7be5a808",
		"--plugin-builder-digest", "sha256:" + strings.Repeat("1", 64),
		"--glibc-max", "2.3.2",
		"--write",
	}
	if out, err := exec.Command(python, args...).CombinedOutput(); err != nil {
		t.Fatalf("assemble bundle: %v\n%s", err, out)
	}

	validate := filepath.Join(root, "deploy", "rebuild", "validate-bundle.py")
	if out, err := exec.Command(python, validate, output).CombinedOutput(); err != nil {
		t.Fatalf("validate bundle: %v\n%s", err, out)
	}

	raw, err := os.ReadFile(filepath.Join(output, "approved-artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, code := verifier.ParseApproval(raw); code != verifier.CodeOK {
		t.Fatalf("update-verifier rejected generated manifest: %s", code)
	}
	var manifest struct {
		SchemaVersion  int `json:"schema_version"`
		VerifierSchema int `json:"verifier_schema"`
		CLI            struct {
			Tag           string `json:"tag"`
			ArchiveSHA256 string `json:"archive_sha256"`
		} `json:"cli"`
		Plugin struct {
			Version      string `json:"version"`
			ABI          int    `json:"abi"`
			RPCSchema    int    `json:"rpc_schema"`
			SourceCommit string `json:"source_commit"`
			Builder      string `json:"builder_digest"`
			GLIBCMax     string `json:"glibc_max"`
		} `json:"plugin"`
		Files []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
			Mode   string `json:"mode"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 2 || manifest.VerifierSchema != 1 || manifest.CLI.Tag != "v7.2.136" ||
		manifest.CLI.ArchiveSHA256 != "8f9160982bc2f26142f7b76a73fcc50f954c453470d5a6aefa81324ad18da288" {
		t.Fatalf("unexpected CLI manifest identity: %+v", manifest.CLI)
	}
	if manifest.Plugin.Version != "1.0.2" || manifest.Plugin.ABI != 1 || manifest.Plugin.RPCSchema != 3 ||
		manifest.Plugin.SourceCommit != "7be5a808" || manifest.Plugin.Builder == "" || manifest.Plugin.GLIBCMax != "2.3.2" {
		t.Fatalf("unexpected plugin manifest identity: %+v", manifest.Plugin)
	}
	if len(manifest.Files) < 10 {
		t.Fatalf("manifest file list too short: %d", len(manifest.Files))
	}
	requiredDeploymentFiles := map[string]bool{
		"deploy/stage-release.sh":    false,
		"deploy/activate-release.sh": false,
		"deploy/rollback-release.sh": false,
		"deploy/validate-bundle.py":  false,
	}
	for _, file := range manifest.Files {
		if file.Path == "" || len(file.SHA256) != sha256.Size*2 || file.Mode == "" {
			t.Fatalf("incomplete manifest file entry: %+v", file)
		}
		if _, err := hex.DecodeString(file.SHA256); err != nil {
			t.Fatalf("invalid file hash %q: %v", file.SHA256, err)
		}
		if _, required := requiredDeploymentFiles[file.Path]; required {
			if file.Mode != "0700" {
				t.Fatalf("deployment file %s mode = %s, want 0700", file.Path, file.Mode)
			}
			requiredDeploymentFiles[file.Path] = true
		}
	}
	for file, found := range requiredDeploymentFiles {
		if !found {
			t.Fatalf("deployment file missing from manifest: %s", file)
		}
	}

}

func TestRebuildBundleOmitsDedicatedPanelArtifact(t *testing.T) {
	python := "python3"
	if runtime.GOOS == "windows" {
		python = "python"
	}
	if _, err := exec.LookPath(python); err != nil {
		t.Skipf("%s unavailable: %v", python, err)
	}

	input := t.TempDir()
	for name, body := range map[string]string{
		"cli-proxy-api":         "\x7fELF\x00fake CLI\n",
		"token-saver-v1.0.2.so": "\x7fELF\x00fake plugin\n",
		"compat-probe":          "\x7fELF\x00fake probe\n",
		"update-verifier":       "\x7fELF\x00fake verifier\n",
	} {
		if err := os.WriteFile(filepath.Join(input, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	output := filepath.Join(t.TempDir(), "bundle")
	root := repositoryRoot(t)
	assemble := filepath.Join(root, "deploy", "rebuild", "assemble-bundle.py")
	args := []string{
		assemble,
		"--input-dir", input,
		"--output-dir", output,
		"--deployment-id", "test-7.2.136-1.0.2",
		"--plugin-source-commit", "7be5a808",
		"--plugin-builder-digest", "sha256:" + strings.Repeat("1", 64),
		"--glibc-max", "2.3.2",
		"--write",
	}
	if out, err := exec.Command(python, args...).CombinedOutput(); err != nil {
		t.Fatalf("assemble panel-free bundle: %v\n%s", err, out)
	}

	raw, err := os.ReadFile(filepath.Join(output, "approved-artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Panel json.RawMessage `json:"panel"`
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Panel != nil {
		t.Fatalf("manifest retained panel identity: %s", manifest.Panel)
	}
	for _, file := range manifest.Files {
		if file.Path == "static/management.html" {
			t.Fatal("bundle retained management.html")
		}
	}
	if _, err := os.Stat(filepath.Join(output, "static", "management.html")); !os.IsNotExist(err) {
		t.Fatalf("management.html stat = %v, want absent", err)
	}
}
