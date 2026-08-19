package deploytests

import (
	"strings"
	"testing"
)

func TestReleaseBuildContractIsCentralizedAndPinned(t *testing.T) {
	makefile := readRepositoryFile(t, "Makefile")
	for _, want := range []string{
		"override VERSION := 1.0.1",
		"CGO_ENABLED=1",
		"CGO_ENABLED=0",
		"GOEXPERIMENT=cgocheck2",
		"-buildmode=c-shared",
		"PluginVersion=$(VERSION)",
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
