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
	ID   string         `yaml:"id"`
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

func requireNamedStep(t *testing.T, job releaseJob, name string) releaseStep {
	t.Helper()
	for _, step := range job.Steps {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("job is missing %q step", name)
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

func TestReleaseWorkflowPublishesOnlyAfterReadOnlyCompatibility(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	build := requireReleaseJob(t, workflow, "build")
	compatibility := requireReleaseJob(t, workflow, "compatibility")
	publish := requireReleaseJob(t, workflow, "publish")

	if workflow.Permissions["contents"] != "read" {
		t.Fatal("workflow must grant read-only contents permission")
	}
	for name, job := range workflow.Jobs {
		for permission, access := range job.Permissions {
			if access == "write" && name != "publish" {
				t.Fatalf("%s job has write permission %s", name, permission)
			}
		}
	}
	if len(workflow.Jobs) != 3 {
		t.Fatalf("release workflow jobs = %d, want build, compatibility, and publish", len(workflow.Jobs))
	}
	if compatibility.Permissions["contents"] != "read" || compatibility.Permissions["actions"] != "read" {
		t.Fatal("compatibility job must have explicit read-only contents and actions permissions")
	}
	if !jobNeeds(compatibility, "build") {
		t.Fatal("compatibility job must wait for build")
	}
	for permission, want := range map[string]string{
		"actions":      "read",
		"attestations": "write",
		"contents":     "write",
		"id-token":     "write",
	} {
		if publish.Permissions[permission] != want {
			t.Fatalf("publish permission %s = %q, want %q", permission, publish.Permissions[permission], want)
		}
	}
	if !jobNeeds(publish, "compatibility") {
		t.Fatal("publish job must wait for compatibility")
	}

	compatibilityRun := joinedRun(compatibility)
	if !strings.Contains(compatibilityRun, "compat-probe") {
		t.Fatal("read-only compatibility job must execute the downloaded candidate")
	}
	dispatch := requireNamedStep(t, compatibility, "Prove real host dispatch on baseline and fixed v7.2.137")
	if !strings.Contains(dispatch.Run, `"$compat_probe" -candidate`) || !strings.Contains(dispatch.Run, "TestRealCandidate") {
		t.Fatal("compatibility job must execute both compatibility probe and real host-dispatch tests")
	}
	publishRun := joinedRun(publish)
	for _, want := range []string{"sha256sum -c SHA256SUMS", "release-metadata.json", "gh release create", "--verify-tag"} {
		if !strings.Contains(publishRun, want) {
			t.Errorf("publish job missing %q", want)
		}
	}
	if strings.Contains(publishRun, "gh release upload") || strings.Contains(publishRun, "--clobber") {
		t.Fatal("publish job must not mutate an existing release")
	}
	for _, step := range publish.Steps {
		if strings.Contains(step.Run, `"$compat_probe"`) || strings.Contains(step.Run, "TestRealCandidate") {
			t.Fatalf("publish step %q runs compatibility after release creation", step.Name)
		}
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

func TestCIWorkflowUsesReadOnlyPermissions(t *testing.T) {
	var workflow releaseWorkflow
	if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github/workflows/ci.yml")), &workflow); err != nil {
		t.Fatalf("parse CI workflow: %v", err)
	}
	if workflow.Permissions["contents"] != "read" {
		t.Fatal("CI workflow must grant read-only contents permission")
	}
	for name, job := range workflow.Jobs {
		for permission, access := range job.Permissions {
			if access == "write" {
				t.Fatalf("CI job %s has write permission %s", name, permission)
			}
		}
	}
}

func TestAllWorkflowCheckoutsDisableCredentialPersistence(t *testing.T) {
	for _, workflowPath := range []string{".github/workflows/ci.yml", ".github/workflows/release.yml"} {
		var workflow releaseWorkflow
		if err := yaml.Unmarshal([]byte(readRepositoryFile(t, workflowPath)), &workflow); err != nil {
			t.Fatalf("parse %s: %v", workflowPath, err)
		}
		for name, job := range workflow.Jobs {
			for _, step := range job.Steps {
				if !strings.HasPrefix(step.Uses, "actions/checkout@") {
					continue
				}
				if workflowValue(step.With, "persist-credentials") != "false" {
					t.Errorf("%s job %s checkout persists credentials", workflowPath, name)
				}
			}
		}
	}
}

func TestReleaseWorkflowAttestsExactlyPublishedArtifacts(t *testing.T) {
	publish := requireReleaseJob(t, readReleaseWorkflow(t), "publish")
	attest := requireActionStep(t, publish, "actions/attest-build-provenance")
	if attest.Uses != "actions/attest-build-provenance@e8998f949152b193b063cb0ec769d69d929409be" {
		t.Fatalf("attestation action = %q", attest.Uses)
	}
	if workflowValue(attest.With, "subject-path") != "dist/*" {
		t.Fatalf("attestation subject = %q, want dist/*", workflowValue(attest.With, "subject-path"))
	}

	attestIndex, publishIndex := -1, -1
	for index, step := range publish.Steps {
		if step.Uses == attest.Uses {
			attestIndex = index
		}
		if strings.Contains(step.Run, "gh release create") {
			publishIndex = index
			if !strings.Contains(step.Run, `gh release create "$GITHUB_REF_NAME" dist/*`) {
				t.Fatal("published files must use the same validated dist/* set as attestation")
			}
		}
	}
	if attestIndex < 0 || publishIndex < 0 || attestIndex >= publishIndex {
		t.Fatal("attestation must complete before release publication")
	}
}

func TestReleaseWorkflowBindsEventBuildMetadataAndRemoteTagCommits(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	build := requireReleaseJob(t, workflow, "build")
	publish := requireReleaseJob(t, workflow, "publish")

	for _, output := range []string{"event_commit", "source_commit"} {
		if !strings.Contains(workflowValue(build.Outputs, output), "steps.release_identity.outputs."+output) {
			t.Errorf("build output %s is not bound to release_identity", output)
		}
	}
	identity := requireNamedStep(t, build, "Bind event and build commits")
	for _, want := range []string{`"${GITHUB_SHA}^{commit}"`, "HEAD^{commit}", `"event_commit=$event_commit"`, `"source_commit=$source_commit"`} {
		if !strings.Contains(identity.Run, want) {
			t.Errorf("build identity step missing %q", want)
		}
	}
	publishIdentity := requireNamedStep(t, publish, "Bind event, build, metadata, and remote tag commits")
	for _, want := range []string{
		"scripts/verify-release-identity.sh",
		"needs.build.outputs.event_commit",
		"needs.build.outputs.source_commit",
		"release-metadata.json",
		"GITHUB_REPOSITORY",
		"GITHUB_REF_NAME",
	} {
		if !strings.Contains(publishIdentity.Run+fmt.Sprint(publishIdentity.Env), want) {
			t.Errorf("publish identity step missing %q", want)
		}
	}
}

func TestReleaseWorkflowVerifiesOfficialHostIdentityBeforeExecution(t *testing.T) {
	compatibility := requireReleaseJob(t, readReleaseWorkflow(t), "compatibility")
	download := requireNamedStep(t, compatibility, "Download and verify official candidates without executing them")
	for _, want := range []string{
		`select(.name == $asset)`,
		`select(.name == "checksums.txt")`,
		".id",
		".size",
		"/releases/assets/${asset_id}",
		"/releases/assets/${checksums_id}",
		`stat -c '%s'`,
		"official_sha256",
		"sha256sum",
		"candidate.tar.gz",
		"checksums.txt",
	} {
		if !strings.Contains(download.Run, want) {
			t.Errorf("official candidate verification missing %q", want)
		}
	}
	for _, ordered := range [][2]string{
		{"asset_id=", "candidate.tar.gz"},
		{"asset_size=", "stat -c '%s'"},
		{"official_sha256=", "tar -xzf"},
	} {
		if strings.Index(download.Run, ordered[0]) >= strings.Index(download.Run, ordered[1]) {
			t.Errorf("%q must occur before %q", ordered[0], ordered[1])
		}
	}
}

func TestReleaseWorkflowUsesFreshImmutableBuildArtifact(t *testing.T) {
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
	uploadPath := workflowValue(upload.With, "path")
	for _, want := range []string{
		"token-saver-v1.0.1-linux-amd64.so",
		"compat-probe-v1.0.1-linux-amd64",
		"update-verifier-v1.0.1-linux-amd64",
		"GLIBC_REQUIREMENTS.txt",
		"release-metadata.json",
		"SHA256SUMS",
	} {
		if !strings.Contains(uploadPath, want) {
			t.Errorf("release upload path missing %q", want)
		}
	}
	for _, forbidden := range []string{"source.tar.gz", "token-saver.h", "\ndist/\n"} {
		if strings.Contains("\n"+uploadPath+"\n", forbidden) {
			t.Fatalf("release upload path includes dirty or broad path %q", forbidden)
		}
	}
	if !strings.Contains(workflowValue(build.Outputs, "manifest_sha256"), "steps.release_manifest.outputs.sha256") {
		t.Fatal("build must bind downstream jobs to the original checksum manifest digest")
	}
	if workflowValue(compatibilityDownload.With, "name") != artifactName {
		t.Fatal("compatibility must download the exact build artifact")
	}
	if workflowValue(publishDownload.With, "name") != artifactName {
		t.Fatal("publish must download the exact compatibility-tested build artifact")
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
