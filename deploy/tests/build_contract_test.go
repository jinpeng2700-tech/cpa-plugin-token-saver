package deploytests

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestReleaseBuildContractIsCentralizedAndPinned(t *testing.T) {
	makefile := readRepositoryFile(t, "Makefile")
	for _, want := range []string{
		"override VERSION := 1.0.1",
		"SOURCE_COMMIT",
		"CGO_ENABLED=1",
		"CGO_ENABLED=0",
		"GOEXPERIMENT=cgocheck2",
		"-buildmode=c-shared",
		"PluginVersion=$(VERSION)",
		"scripts/archive-source.sh",
		"scripts/finalize-release.sh",
		"tar -xf - -C",
		`--file "$$temporary/build/release.Dockerfile"`,
	} {
		if !strings.Contains(makefile, want) {
			t.Errorf("Makefile missing %q", want)
		}
	}

	dockerfile := readRepositoryFile(t, "build/release.Dockerfile")
	for _, want := range []string{
		"golang:1.26.5@sha256:",
		"golang:1.20-bullseye@sha256:",
		"COPY --from=go126 /usr/local/go /usr/local/go",
		"make release",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("release builder missing %q", want)
		}
	}

	for _, workflowPath := range []string{".github/workflows/ci.yml", ".github/workflows/release.yml"} {
		workflow := readRepositoryFile(t, workflowPath)
		if !strings.Contains(workflow, "make ") {
			t.Errorf("%s does not use Makefile entry points", workflowPath)
		}
		if strings.Contains(workflow, "go build ") || strings.Contains(workflow, "CGO_ENABLED=") {
			t.Errorf("%s duplicates release build parameters", workflowPath)
		}
	}
	if strings.Contains(makefile, "--file '$(RELEASE_DOCKERFILE)'") {
		t.Fatal("release container must use Dockerfile extracted from the committed source archive")
	}
}

func TestReleaseSourceArchiveUsesCommittedFilesOnly(t *testing.T) {
	requireTool(t, "git")
	requireTool(t, "sh")

	repository := t.TempDir()
	runTestCommand(t, repository, "git", "init", "-q")
	runTestCommand(t, repository, "git", "config", "user.email", "release-test@example.invalid")
	runTestCommand(t, repository, "git", "config", "user.name", "Release Test")
	writeTestFile(t, filepath.Join(repository, "source.go"), "package source\nconst Version = \"committed\"\n")
	runTestCommand(t, repository, "git", "add", "source.go")
	runTestCommand(t, repository, "git", "commit", "-q", "-m", "fixture")
	runTestCommand(t, repository, "git", "config", "core.autocrlf", "true")

	script := filepath.Join(repositoryRoot(t), "scripts", "archive-source.sh")
	clean := runTestCommand(t, repository, "sh", script, repository, "HEAD")
	reader := tar.NewReader(bytes.NewReader(clean))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			t.Fatal("committed source archive is missing source.go")
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name != "source.go" {
			continue
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(content, []byte("\r\n")) {
			t.Fatal("committed source archive depends on local core.autocrlf")
		}
		break
	}

	writeTestFile(t, filepath.Join(repository, "source.go"), "package source\nconst Version = \"dirty\"\n")
	for _, name := range []string{
		"compat-probe",
		"update-verifier",
		"token-saver.so",
		"token-saver.h",
		"source.tar.gz",
		filepath.Join("deploy", "tests", "__pycache__", "fixture.pyc"),
	} {
		writeTestFile(t, filepath.Join(repository, name), "dirty output\n")
	}
	dirty := runTestCommand(t, repository, "sh", script, repository, "HEAD")

	if !bytes.Equal(clean, dirty) {
		t.Fatal("dirty tracked or untracked files changed committed-source archive")
	}
}

func TestReleaseFinalizerEmitsPortableMetadataAndChecksums(t *testing.T) {
	dist, env := releaseFinalizerFixture(t, "2.32", false)
	runReleaseFinalizer(t, dist, env, true)

	wantFiles := []string{
		"GLIBC_REQUIREMENTS.txt",
		"SHA256SUMS",
		"compat-probe-v1.0.1-linux-amd64",
		"release-metadata.json",
		"token-saver-v1.0.1-linux-amd64.so",
		"update-verifier-v1.0.1-linux-amd64",
	}
	entries, err := os.ReadDir(dist)
	if err != nil {
		t.Fatal(err)
	}
	var gotFiles []string
	for _, entry := range entries {
		gotFiles = append(gotFiles, entry.Name())
	}
	slices.Sort(gotFiles)
	if !slices.Equal(gotFiles, wantFiles) {
		t.Fatalf("release files = %v, want %v", gotFiles, wantFiles)
	}

	var metadata struct {
		Version      string `json:"version"`
		Tag          string `json:"tag"`
		SourceCommit string `json:"source_commit"`
		Platform     string `json:"platform"`
		ABI          int    `json:"abi"`
		RPC          int    `json:"rpc"`
		GLIBCMax     string `json:"glibc_max"`
	}
	raw, err := os.ReadFile(filepath.Join(dist, "release-metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatalf("decode release metadata: %v", err)
	}
	if metadata.Version != "1.0.1" || metadata.Tag != "v1.0.1" ||
		metadata.SourceCommit != strings.Repeat("a", 40) || metadata.Platform != "linux-amd64" ||
		metadata.ABI != 1 || metadata.RPC != 3 || metadata.GLIBCMax != "2.32" {
		t.Fatalf("release metadata = %#v", metadata)
	}
	if output := runTestCommand(t, dist, "sha256sum", "-c", "SHA256SUMS"); !strings.Contains(string(output), "OK") {
		t.Fatalf("checksum verification output = %s", output)
	}
}

func TestReleaseFinalizerRejectsNewerGLIBCAndDynamicHelpers(t *testing.T) {
	for _, tt := range []struct {
		name          string
		glibc         string
		dynamicHelper bool
		want          string
	}{
		{name: "GLIBC 2.34", glibc: "2.34", want: "GLIBC ceiling 2.32"},
		{name: "dynamic helper", glibc: "2.32", dynamicHelper: true, want: "dynamically linked helper"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dist, env := releaseFinalizerFixture(t, tt.glibc, tt.dynamicHelper)
			output := runReleaseFinalizer(t, dist, env, false)
			if !strings.Contains(string(output), tt.want) {
				t.Fatalf("failure output = %s, want %q", output, tt.want)
			}
		})
	}
}

func TestWorkflowActionsUseReviewedFullCommitSHAs(t *testing.T) {
	fullSHA := regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)
	for _, workflowPath := range []string{".github/workflows/ci.yml", ".github/workflows/release.yml"} {
		workflow := readRepositoryFile(t, workflowPath)
		for _, line := range strings.Split(workflow, "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "- uses:") {
				continue
			}
			action := strings.Fields(strings.TrimSpace(strings.TrimPrefix(trimmed, "- uses:")))[0]
			if !fullSHA.MatchString(action) {
				t.Errorf("%s action is not pinned to a full commit SHA: %s", workflowPath, action)
			}
		}
	}
}

func TestReleaseWorkflowPinsVersionHostAndLinuxAMD64(t *testing.T) {
	workflow := readRepositoryFile(t, ".github/workflows/release.yml")
	for _, want := range []string{
		`tags:`,
		`- "v1.0.1"`,
		"v7.2.136",
		"linux-amd64",
	} {
		if !strings.Contains(workflow, want) {
			t.Errorf("release workflow missing %q", want)
		}
	}
	if strings.Contains(workflow, "releases/latest") || strings.Contains(workflow, "linux-arm64") {
		t.Fatal("release workflow must not drift to latest host or build arm64")
	}
}

func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s unavailable: %v", name, err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func runTestCommand(t *testing.T, directory, name string, args ...string) []byte {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return output
}

func releaseFinalizerFixture(t *testing.T, glibc string, dynamicHelper bool) (string, []string) {
	t.Helper()
	requireTool(t, "sh")
	requireTool(t, "sha256sum")

	dist := t.TempDir()
	for _, name := range []string{
		"token-saver-v1.0.1-linux-amd64.so",
		"compat-probe-v1.0.1-linux-amd64",
		"update-verifier-v1.0.1-linux-amd64",
	} {
		writeTestFile(t, filepath.Join(dist, name), name+"\n")
	}

	tools := t.TempDir()
	readelf := filepath.Join(tools, "readelf")
	objdump := filepath.Join(tools, "objdump")
	writeTestFile(t, readelf, `#!/bin/sh
case "$2" in
  *.so) printf '  Class: ELF64\n  Machine: Advanced Micro Devices X86-64\n  Type: DYN (Shared object file)\n' ;;
  *) printf '  Class: ELF64\n  Machine: Advanced Micro Devices X86-64\n  Type: EXEC (Executable file)\n' ;;
esac
`)
	needed := ""
	if dynamicHelper {
		needed = "printf '  NEEDED               libc.so.6\\n'"
	}
	writeTestFile(t, objdump, `#!/bin/sh
case "$1" in
  -T) printf '0000000000000000 g DF .text 0000000000000000 GLIBC_`+glibc+` fixture\n' ;;
  -p) `+needed+` ;;
esac
`)
	if runtime.GOOS != "windows" {
		if err := os.Chmod(readelf, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(objdump, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := append(os.Environ(), "READELF="+readelf, "OBJDUMP="+objdump)
	return dist, env
}

func runReleaseFinalizer(t *testing.T, dist string, env []string, wantSuccess bool) []byte {
	t.Helper()
	script := filepath.Join(repositoryRoot(t), "scripts", "finalize-release.sh")
	command := exec.Command("sh", script, dist, "1.0.1", strings.Repeat("a", 40))
	command.Env = env
	output, err := command.CombinedOutput()
	if wantSuccess && err != nil {
		t.Fatalf("finalize release: %v\n%s", err, output)
	}
	if !wantSuccess && err == nil {
		t.Fatalf("finalize release unexpectedly succeeded\n%s", output)
	}
	return output
}
