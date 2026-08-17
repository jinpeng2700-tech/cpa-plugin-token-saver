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

func TestReleaseWorkflowIsolatesCandidateFromPublisher(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	build := requireReleaseJob(t, workflow, "build")
	compatibility := requireReleaseJob(t, workflow, "compatibility")
	publish := requireReleaseJob(t, workflow, "publish")

	if workflow.Permissions["contents"] == "write" {
		t.Fatal("contents: write must not be granted at workflow scope")
	}
	for name, job := range workflow.Jobs {
		if job.Permissions["contents"] == "write" && name != "publish" {
			t.Fatalf("%s job has contents: write; only publish may write releases", name)
		}
	}
	if publish.Permissions["contents"] != "write" {
		t.Fatal("publish job must own the sole contents: write permission")
	}
	for permission, access := range compatibility.Permissions {
		if access == "write" {
			t.Fatalf("compatibility job has write permission %s", permission)
		}
	}
	if compatibility.Permissions["contents"] != "read" || compatibility.Permissions["actions"] != "read" {
		t.Fatal("compatibility job must have explicit read-only contents and actions permissions")
	}
	if !jobNeeds(publish, "build") || !jobNeeds(publish, "compatibility") {
		t.Fatal("publish job must wait for both build and compatibility")
	}

	compatibilityRun := joinedRun(compatibility)
	publishRun := joinedRun(publish)
	if !strings.Contains(compatibilityRun, "compat-probe") || strings.Contains(publishRun, "compat-probe") {
		t.Fatal("only the read-only compatibility job may execute the downloaded candidate")
	}
	if !strings.Contains(publishRun, "gh release") || strings.Contains(compatibilityRun, "gh release") {
		t.Fatal("only the write-authorized publish job may invoke gh release")
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
	_ = build
}

func TestReleaseWorkflowPublishesFreshImmutableBuildArtifact(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	build := requireReleaseJob(t, workflow, "build")
	compatibility := requireReleaseJob(t, workflow, "compatibility")
	publish := requireReleaseJob(t, workflow, "publish")

	upload := requireActionStep(t, build, "actions/upload-artifact")
	compatibilityDownload := requireActionStep(t, compatibility, "actions/download-artifact")
	publishDownload := requireActionStep(t, publish, "actions/download-artifact")
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
	if workflowValue(compatibilityDownload.With, "name") != artifactName || workflowValue(publishDownload.With, "name") != artifactName {
		t.Fatal("compatibility and publish must independently download the exact build artifact")
	}
	compatibilityPath := workflowValue(compatibilityDownload.With, "path")
	publishPath := workflowValue(publishDownload.With, "path")
	if compatibilityPath == "" || publishPath == "" || compatibilityPath == publishPath {
		t.Fatal("candidate and publisher must download the artifact into different job-local paths")
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
	if !strings.Contains(joinedRun(publish), "sha256sum -c SHA256SUMS") {
		t.Fatal("publisher must verify the freshly downloaded build artifact before release")
	}
	if !strings.Contains(joinedRun(publish), "EXPECTED_MANIFEST_SHA256") {
		t.Fatal("publisher must compare the downloaded manifest with the build job output")
	}
}
