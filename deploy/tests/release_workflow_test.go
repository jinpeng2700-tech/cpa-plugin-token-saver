package deploytests

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type releaseWorkflow struct {
	Permissions map[string]string     `yaml:"permissions"`
	Jobs        map[string]releaseJob `yaml:"jobs"`
}

type releaseJob struct {
	Needs       any               `yaml:"needs"`
	Outputs     map[string]any    `yaml:"outputs"`
	Permissions map[string]string `yaml:"permissions"`
	Steps       []releaseStep     `yaml:"steps"`
}

type releaseStep struct {
	Name string         `yaml:"name"`
	Uses string         `yaml:"uses"`
	Run  string         `yaml:"run"`
	Env  map[string]any `yaml:"env"`
	With map[string]any `yaml:"with"`
}

func readReleaseWorkflow(t *testing.T) releaseWorkflow {
	t.Helper()
	var workflow releaseWorkflow
	if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github/workflows/release.yml")), &workflow); err != nil {
		t.Fatalf("parse release workflow: %v", err)
	}
	return workflow
}

func requireReleaseJob(t *testing.T, workflow releaseWorkflow, name string) releaseJob {
	t.Helper()
	job, ok := workflow.Jobs[name]
	if !ok {
		t.Fatalf("release workflow is missing %q job", name)
	}
	return job
}

func requireActionStep(t *testing.T, job releaseJob, action string) releaseStep {
	t.Helper()
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, action+"@") {
			return step
		}
	}
	t.Fatalf("job is missing %s action", action)
	return releaseStep{}
}

func joinedRun(job releaseJob) string {
	var commands []string
	for _, step := range job.Steps {
		commands = append(commands, step.Run)
	}
	return strings.Join(commands, "\n")
}

func workflowValue(values map[string]any, key string) string {
	return fmt.Sprint(values[key])
}

func jobNeeds(job releaseJob, dependency string) bool {
	switch needs := job.Needs.(type) {
	case string:
		return needs == dependency
	case []any:
		for _, need := range needs {
			if fmt.Sprint(need) == dependency {
				return true
			}
		}
	}
	return false
}

func TestReleaseWorkflowRunsReadOnlyCompatibilityWithoutPublishing(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	build := requireReleaseJob(t, workflow, "build")
	compatibility := requireReleaseJob(t, workflow, "compatibility")

	if workflow.Permissions["contents"] != "read" {
		t.Fatal("workflow must grant read-only contents permission")
	}
	for name, job := range workflow.Jobs {
		for permission, access := range job.Permissions {
			if access == "write" {
				t.Fatalf("%s job has write permission %s", name, permission)
			}
		}
	}
	if _, exists := workflow.Jobs["publish"]; exists {
		t.Fatal("selected deployment plan forbids a GitHub release publisher")
	}
	if len(workflow.Jobs) != 2 {
		t.Fatalf("release workflow jobs = %d, want build and compatibility only", len(workflow.Jobs))
	}
	if compatibility.Permissions["contents"] != "read" || compatibility.Permissions["actions"] != "read" {
		t.Fatal("compatibility job must have explicit read-only contents and actions permissions")
	}
	if !jobNeeds(compatibility, "build") {
		t.Fatal("compatibility job must wait for build")
	}

	compatibilityRun := joinedRun(compatibility)
	if !strings.Contains(compatibilityRun, "compat-probe") {
		t.Fatal("read-only compatibility job must execute the downloaded candidate")
	}
	if strings.Contains(readRepositoryFile(t, ".github/workflows/release.yml"), "gh release") {
		t.Fatal("release workflow must not publish a GitHub release")
	}
	checkout := requireActionStep(t, compatibility, "actions/checkout")
	if workflowValue(checkout.With, "persist-credentials") != "false" {
		t.Fatal("compatibility checkout must not persist even its read-only token")
	}
	for _, step := range compatibility.Steps {
		if !strings.Contains(step.Run, "compat-probe") && !strings.Contains(step.Run, "TestRealCandidate") {
			continue
		}
		if _, ok := step.Env["GH_TOKEN"]; ok || strings.Contains(step.Run, "GH_TOKEN") || strings.Contains(step.Run, "github.token") || strings.Contains(step.Run, "secrets.") {
			t.Fatalf("candidate execution step %q exposes a repository token or secret", step.Name)
		}
	}
	ci := readRepositoryFile(t, ".github/workflows/ci.yml")
	if !strings.Contains(ci, "tags-ignore:") || !strings.Contains(ci, `- "v*"`) {
		t.Fatal("ordinary CI must ignore release tags to avoid duplicate builds")
	}
	_ = build
}

func TestReleaseWorkflowUsesFreshImmutableBuildArtifact(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	build := requireReleaseJob(t, workflow, "build")
	compatibility := requireReleaseJob(t, workflow, "compatibility")

	upload := requireActionStep(t, build, "actions/upload-artifact")
	compatibilityDownload := requireActionStep(t, compatibility, "actions/download-artifact")
	artifactName := workflowValue(upload.With, "name")
	if artifactName == "" || !strings.Contains(artifactName, "github.run_id") || !strings.Contains(artifactName, "github.run_attempt") {
		t.Fatalf("build artifact name %q is not unique to this run attempt", artifactName)
	}
	if workflowValue(upload.With, "overwrite") == "true" {
		t.Fatal("release build artifact must be immutable")
	}
	if !strings.Contains(workflowValue(build.Outputs, "manifest_sha256"), "steps.release_manifest.outputs.sha256") {
		t.Fatal("build must bind downstream jobs to the original checksum manifest digest")
	}
	if workflowValue(compatibilityDownload.With, "name") != artifactName {
		t.Fatal("compatibility must download the exact build artifact")
	}

	compatibilityRun := joinedRun(compatibility)
	if !strings.Contains(compatibilityRun, `candidate_root="$RUNNER_TEMP/candidate-artifact"`) ||
		!strings.Contains(compatibilityRun, `cp -a "$source_root/." "$candidate_root/"`) ||
		!strings.Contains(compatibilityRun, `rm -rf "$source_root"`) {
		t.Fatal("compatibility must execute only a disposable copy of its downloaded artifact")
	}
	if !strings.Contains(compatibilityRun, `token-saver.so`) {
		t.Fatal("real compatibility probe must exercise the production stable plugin filename")
	}
	if !strings.Contains(compatibilityRun, "sha256sum -c SHA256SUMS") {
		t.Fatal("compatibility must verify the freshly downloaded build artifact")
	}
	if !strings.Contains(compatibilityRun, "EXPECTED_MANIFEST_SHA256") {
		t.Fatal("compatibility must compare the downloaded manifest with the build job output")
	}
}
