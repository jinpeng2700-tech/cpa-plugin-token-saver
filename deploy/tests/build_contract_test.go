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
		"override VERSION := 1.2.4",
		"SOURCE_COMMIT",
		"CGO_ENABLED=1",
		"CGO_ENABLED=0",
		"GOEXPERIMENT=cgocheck2",
		"-buildmode=c-shared",
		"PluginVersion=$(VERSION)",
		"scripts/release-container.sh",
		"scripts/finalize-release.sh",
		"! readelf -l '$(COMPAT_PROBE_OUT)' | grep -q 'INTERP'",
		"! readelf -l '$(UPDATE_VERIFIER_OUT)' | grep -q 'INTERP'",
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
		if strings.Contains(workflow, "make release-container") {
			t.Errorf("%s executes release driver from the working tree", workflowPath)
		}
		if !strings.Contains(workflow, `git -c core.autocrlf=false show "HEAD:scripts/release-container.sh"`) {
			t.Errorf("%s does not load the release driver from the committed source", workflowPath)
		}
	}
	if strings.Contains(makefile, "--file '$(RELEASE_DOCKERFILE)'") {
		t.Fatal("release container must use Dockerfile extracted from the committed source archive")
	}
	if strings.HasPrefix(strings.TrimSpace(dockerfile), "# syntax=docker/dockerfile:1") {
		t.Fatal("release Dockerfile must not use a mutable frontend syntax tag")
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

func TestCommittedReleaseDriverIgnoresDirtyWorkingCopy(t *testing.T) {
	requireTool(t, "git")
	requireTool(t, "sh")

	repository := t.TempDir()
	runTestCommand(t, repository, "git", "init", "-q")
	runTestCommand(t, repository, "git", "config", "user.email", "release-test@example.invalid")
	runTestCommand(t, repository, "git", "config", "user.name", "Release Test")
	driverPath := filepath.Join(repository, "scripts", "release-container.sh")
	writeTestFile(t, driverPath, "#!/bin/sh\nprintf 'committed\\n'\n")
	runTestCommand(t, repository, "git", "add", "scripts/release-container.sh")
	runTestCommand(t, repository, "git", "commit", "-q", "-m", "fixture")
	writeTestFile(t, driverPath, "#!/bin/sh\nprintf 'dirty\\n'\n")

	committed := runTestCommand(t, repository, "git", "-c", "core.autocrlf=false", "show", "HEAD:scripts/release-container.sh")
	command := exec.Command("sh", "-s")
	command.Stdin = bytes.NewReader(committed)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute committed release driver: %v\n%s", err, output)
	}
	if string(output) != "committed\n" {
		t.Fatalf("release driver output = %q, want committed source", output)
	}

	info, err := os.Stat(filepath.Join(repositoryRoot(t), "scripts", "release-container.sh"))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("repository committed release driver is missing: %v", err)
	}
}

func TestReleaseIdentityVerifierRequiresSamePeeledCommit(t *testing.T) {
	requireTool(t, "git")
	requireTool(t, "sh")

	repository := t.TempDir()
	runTestCommand(t, repository, "git", "init", "-q")
	runTestCommand(t, repository, "git", "config", "user.email", "release-test@example.invalid")
	runTestCommand(t, repository, "git", "config", "user.name", "Release Test")
	writeTestFile(t, filepath.Join(repository, "source.txt"), "first\n")
	runTestCommand(t, repository, "git", "add", "source.txt")
	runTestCommand(t, repository, "git", "commit", "-q", "-m", "first")
	first := strings.TrimSpace(string(runTestCommand(t, repository, "git", "rev-parse", "HEAD")))
	runTestCommand(t, repository, "git", "tag", "-a", "v1.2.4", "-m", "release")

	writeTestFile(t, filepath.Join(repository, "source.txt"), "second\n")
	runTestCommand(t, repository, "git", "commit", "-qam", "second")
	second := strings.TrimSpace(string(runTestCommand(t, repository, "git", "rev-parse", "HEAD")))
	remote := filepath.Join(t.TempDir(), "remote.git")
	runTestCommand(t, repository, "git", "clone", "-q", "--bare", repository, remote)

	metadata := filepath.Join(t.TempDir(), "release-metadata.json")
	writeMetadata := func(commit string) {
		t.Helper()
		writeTestFile(t, metadata, `{"source_commit":"`+commit+`"}`+"\n")
	}
	writeMetadata(first)
	script := filepath.Join(repositoryRoot(t), "scripts", "verify-release-identity.sh")
	command := exec.Command("sh", script, first, first, metadata, remote, "v1.2.4")
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("matching release identity rejected: %v\n%s", err, output)
	}

	for _, tt := range []struct {
		name     string
		event    string
		build    string
		metadata string
	}{
		{name: "event mismatch", event: second, build: first, metadata: first},
		{name: "build mismatch", event: first, build: second, metadata: first},
		{name: "metadata mismatch", event: first, build: first, metadata: second},
		{name: "remote tag mismatch", event: second, build: second, metadata: second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			writeMetadata(tt.metadata)
			command := exec.Command("sh", script, tt.event, tt.build, metadata, remote, "v1.2.4")
			command.Dir = repository
			if output, err := command.CombinedOutput(); err == nil || !strings.Contains(string(output), "release commit identity mismatch") {
				t.Fatalf("mismatch result: err=%v output=%s", err, output)
			}
		})
	}
}

func TestReleaseFinalizerEmitsPortableMetadataAndChecksums(t *testing.T) {
	dist, env := releaseFinalizerFixture(t, "2.32", false, false)
	runReleaseFinalizer(t, dist, env, true)

	wantFiles := []string{
		"GLIBC_REQUIREMENTS.txt",
		"SHA256SUMS",
		"compat-probe-v1.2.4-linux-amd64",
		"release-metadata.json",
		"token-saver-v1.2.4-linux-amd64.so",
		"update-verifier-v1.2.4-linux-amd64",
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
	if metadata.Version != "1.2.4" || metadata.Tag != "v1.2.4" ||
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
		interpHelper  bool
		want          string
	}{
		{name: "GLIBC 2.34", glibc: "2.34", want: "GLIBC ceiling 2.32"},
		{name: "dynamic helper", glibc: "2.32", dynamicHelper: true, want: "dynamically linked helper"},
		{name: "PT_INTERP helper", glibc: "2.32", interpHelper: true, want: "PT_INTERP"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dist, env := releaseFinalizerFixture(t, tt.glibc, tt.dynamicHelper, tt.interpHelper)
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
		`- "v1.2.4"`,
		"v7.2.137",
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

func releaseFinalizerFixture(t *testing.T, glibc string, dynamicHelper, interpHelper bool) (string, []string) {
	t.Helper()
	requireTool(t, "sh")
	requireTool(t, "sha256sum")

	dist := t.TempDir()
	for _, name := range []string{
		"token-saver-v1.2.4-linux-amd64.so",
		"compat-probe-v1.2.4-linux-amd64",
		"update-verifier-v1.2.4-linux-amd64",
	} {
		writeTestFile(t, filepath.Join(dist, name), name+"\n")
	}

	tools := t.TempDir()
	readelf := filepath.Join(tools, "readelf")
	objdump := filepath.Join(tools, "objdump")
	interp := ""
	if interpHelper {
		interp = "printf '  INTERP         0x0000000000000000\\n'"
	}
	writeTestFile(t, readelf, `#!/bin/sh
case "$1" in
  -h)
    case "$2" in
      *.so) printf '  Class: ELF64\n  Machine: Advanced Micro Devices X86-64\n  Type: DYN (Shared object file)\n' ;;
      *) printf '  Class: ELF64\n  Machine: Advanced Micro Devices X86-64\n  Type: EXEC (Executable file)\n' ;;
    esac
    ;;
  -l) `+interp+` ;;
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
	command := exec.Command("sh", script, dist, "1.2.4", strings.Repeat("a", 40))
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
