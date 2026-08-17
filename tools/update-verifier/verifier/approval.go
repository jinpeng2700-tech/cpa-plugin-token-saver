package verifier

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

var lowerSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func ParseApproval(raw []byte) (Approval, string) {
	var approval Approval
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&approval); errDecode != nil {
		return Approval{}, CodeApprovalInvalid
	}
	var trailing any
	if errTrailing := decoder.Decode(&trailing); errTrailing != io.EOF {
		return Approval{}, CodeApprovalInvalid
	}
	if approval.SchemaVersion != ApprovalSchemaVersion {
		return Approval{}, CodeApprovalSchema
	}
	if approval.VerifierSchema != VerifierSchemaVersion {
		return Approval{}, CodeVerifierSchema
	}
	if approval.Plugin.ABI != 1 {
		return Approval{}, CodeApprovalABI
	}
	if approval.Plugin.RPC != 3 {
		return Approval{}, CodeApprovalRPC
	}
	if !validVersion(approval.CLI.Version) || !validVersion(approval.Plugin.Version) || !validVersion(approval.Panel.Version) ||
		(approval.CLI.Arch != "linux-amd64" && approval.CLI.Arch != "linux-arm64") ||
		!lowerSHA256Pattern.MatchString(approval.CLI.SHA256) ||
		!lowerSHA256Pattern.MatchString(approval.Plugin.SHA256) ||
		!lowerSHA256Pattern.MatchString(approval.Panel.SHA256) {
		return Approval{}, CodeApprovalInvalid
	}
	return approval, CodeOK
}

func VerifyArtifacts(phase Phase, approval Approval, artifacts Artifacts) Result {
	if artifacts.Arch != approval.CLI.Arch {
		return phaseFailure(phase, CodeArchitectureMismatch)
	}
	if !hashEqual(artifacts.CLIHash, approval.CLI.SHA256) {
		return phaseFailure(phase, CodeCLIHashMismatch)
	}
	if !hashEqual(artifacts.PluginHash, approval.Plugin.SHA256) {
		return phaseFailure(phase, CodePluginHashMismatch)
	}
	if !hashEqual(artifacts.PanelHash, approval.Panel.SHA256) {
		return phaseFailure(phase, CodePanelHashMismatch)
	}
	return compatible()
}

func validVersion(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, "\r\n\x00")
}

func hashEqual(actual, approved string) bool {
	if len(actual) != len(approved) || len(approved) != 64 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(actual), []byte(approved)) == 1
}
