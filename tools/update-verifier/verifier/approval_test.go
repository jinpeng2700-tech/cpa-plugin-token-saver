package verifier

import (
	"strings"
	"testing"
)

func validApproval() Approval {
	return Approval{
		SchemaVersion:  ApprovalSchemaVersion,
		VerifierSchema: VerifierSchemaVersion,
		CLI: ApprovedCLI{
			Version: "v7.2.136",
			SHA256:  strings.Repeat("a", 64),
			Arch:    "linux-amd64",
		},
		Plugin: ApprovedPlugin{
			Version: "1.0.1",
			SHA256:  strings.Repeat("b", 64),
			ABI:     1,
			RPC:     3,
		},
	}
}

func TestParseApprovalRejectsMissingInvalidAndUnknownData(t *testing.T) {
	valid := validApproval()
	validJSON := `{"schema_version":2,"verifier_schema":1,"bundle":{"deployment_id":"test-7.2.136-1.0.1","source_commit":"7be5a808","builder_digest":"sha256:` + strings.Repeat("1", 64) + `"},"cli":{"tag":"v7.2.136","archive_sha256":"8f9160982bc2f26142f7b76a73fcc50f954c453470d5a6aefa81324ad18da288","binary_sha256":"` + valid.CLI.SHA256 + `","arch":"linux-amd64"},"plugin":{"id":"token-saver","version":"1.0.1","abi":1,"rpc_schema":3,"source_commit":"7be5a808","builder_digest":"sha256:` + strings.Repeat("1", 64) + `","glibc_max":"2.3.2","sha256":"` + valid.Plugin.SHA256 + `"},"files":[{"path":"cli-proxy-api","sha256":"` + valid.CLI.SHA256 + `","mode":"0700"},{"path":"plugins/linux/amd64/token-saver-v1.0.1.so","sha256":"` + valid.Plugin.SHA256 + `","mode":"0700"}],"manifest_exclusions":["approved-artifacts.json","SHA256SUMS"]}`

	for _, tt := range []struct {
		name string
		raw  string
		code string
	}{
		{name: "missing", raw: "", code: CodeApprovalInvalid},
		{name: "malformed", raw: "{", code: CodeApprovalInvalid},
		{name: "unknown field", raw: strings.TrimSuffix(validJSON, "}") + `,"transport_checksum":"self-authorizing"}`, code: CodeApprovalInvalid},
		{name: "approval schema", raw: strings.Replace(validJSON, `"schema_version":2`, `"schema_version":3`, 1), code: CodeApprovalSchema},
		{name: "verifier schema", raw: strings.Replace(validJSON, `"verifier_schema":1`, `"verifier_schema":2`, 1), code: CodeVerifierSchema},
		{name: "wrong ABI", raw: strings.Replace(validJSON, `"abi":1`, `"abi":2`, 1), code: CodeApprovalABI},
		{name: "bad hash", raw: strings.Replace(validJSON, valid.Plugin.SHA256, "not-a-hash", 1), code: CodeApprovalInvalid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, code := ParseApproval([]byte(tt.raw))
			if code != tt.code {
				t.Fatalf("ParseApproval() code = %q, want %q", code, tt.code)
			}
		})
	}

	got, code := ParseApproval([]byte(validJSON))
	if code != CodeOK || got != valid {
		t.Fatalf("ParseApproval(valid) = %#v, %q; want %#v, %q", got, code, valid, CodeOK)
	}
}

func TestVerifyArtifactsClassifiesWrongHashAndArchitecture(t *testing.T) {
	approval := validApproval()
	valid := Artifacts{Arch: approval.CLI.Arch, CLIHash: approval.CLI.SHA256, PluginHash: approval.Plugin.SHA256}

	for _, tt := range []struct {
		name   string
		mutate func(*Artifacts)
		code   string
	}{
		{name: "architecture", mutate: func(a *Artifacts) { a.Arch = "linux-arm64" }, code: CodeArchitectureMismatch},
		{name: "CLI hash", mutate: func(a *Artifacts) { a.CLIHash = strings.Repeat("d", 64) }, code: CodeCLIHashMismatch},
		{name: "plugin hash", mutate: func(a *Artifacts) { a.PluginHash = strings.Repeat("d", 64) }, code: CodePluginHashMismatch},
	} {
		t.Run(tt.name, func(t *testing.T) {
			artifacts := valid
			tt.mutate(&artifacts)
			result := VerifyArtifacts(PhasePostInstall, approval, artifacts)
			if result.Compatible || result.Classification != ClassificationCandidateFailure || result.Code != tt.code {
				t.Fatalf("VerifyArtifacts() = %#v", result)
			}
		})
	}

	if result := VerifyArtifacts(PhasePostInstall, approval, valid); !result.Compatible || result.Code != CodeOK {
		t.Fatalf("VerifyArtifacts(valid) = %#v", result)
	}
}
