package verifier

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var lowerSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var lowerCommitPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var modePattern = regexp.MustCompile(`^0[0-7]{3}$`)
var deploymentIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type approvalDocument struct {
	SchemaVersion      int            `json:"schema_version"`
	VerifierSchema     int            `json:"verifier_schema"`
	Bundle             approvalBundle `json:"bundle"`
	CLI                approvalCLI    `json:"cli"`
	Plugin             approvalPlugin `json:"plugin"`
	Files              []approvalFile `json:"files"`
	ManifestExclusions []string       `json:"manifest_exclusions"`
}

type approvalBundle struct {
	DeploymentID string `json:"deployment_id"`
	SourceCommit string `json:"source_commit"`
	Builder      string `json:"builder_digest"`
}

type approvalCLI struct {
	Tag           string `json:"tag"`
	ArchiveSHA256 string `json:"archive_sha256"`
	BinarySHA256  string `json:"binary_sha256"`
	Arch          string `json:"arch"`
}

type approvalPlugin struct {
	ID           string `json:"id"`
	Version      string `json:"version"`
	ABI          uint32 `json:"abi"`
	RPCSchema    uint32 `json:"rpc_schema"`
	SourceCommit string `json:"source_commit"`
	Builder      string `json:"builder_digest"`
	GLIBCMax     string `json:"glibc_max"`
	SHA256       string `json:"sha256"`
}

type approvalFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode"`
}

func ParseApproval(raw []byte) (Approval, string) {
	var document approvalDocument
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&document); errDecode != nil {
		return Approval{}, CodeApprovalInvalid
	}
	var trailing any
	if errTrailing := decoder.Decode(&trailing); errTrailing != io.EOF {
		return Approval{}, CodeApprovalInvalid
	}
	if document.SchemaVersion != ApprovalSchemaVersion {
		return Approval{}, CodeApprovalSchema
	}
	if document.VerifierSchema != VerifierSchemaVersion {
		return Approval{}, CodeVerifierSchema
	}
	if document.Plugin.ABI != 1 {
		return Approval{}, CodeApprovalABI
	}
	if document.Plugin.RPCSchema != 3 {
		return Approval{}, CodeApprovalRPC
	}
	if !validApprovalDocument(document) {
		return Approval{}, CodeApprovalInvalid
	}
	return Approval{
		SchemaVersion:  document.SchemaVersion,
		VerifierSchema: document.VerifierSchema,
		CLI: ApprovedCLI{
			Version: document.CLI.Tag,
			SHA256:  document.CLI.BinarySHA256,
			Arch:    document.CLI.Arch,
		},
		Plugin: ApprovedPlugin{
			Version: document.Plugin.Version,
			SHA256:  document.Plugin.SHA256,
			ABI:     document.Plugin.ABI,
			RPC:     document.Plugin.RPCSchema,
		},
	}, CodeOK
}

func validApprovalDocument(document approvalDocument) bool {
	if !deploymentIDPattern.MatchString(document.Bundle.DeploymentID) ||
		!lowerCommitPattern.MatchString(document.Bundle.SourceCommit) ||
		!digestPattern.MatchString(document.Bundle.Builder) ||
		document.CLI.Tag != "v7.2.136" ||
		document.CLI.Arch != "linux-amd64" ||
		document.CLI.ArchiveSHA256 != "8f9160982bc2f26142f7b76a73fcc50f954c453470d5a6aefa81324ad18da288" ||
		!lowerSHA256Pattern.MatchString(document.CLI.BinarySHA256) ||
		document.Plugin.ID != "token-saver" ||
		document.Plugin.Version != "1.0.2" ||
		!lowerCommitPattern.MatchString(document.Plugin.SourceCommit) ||
		!digestPattern.MatchString(document.Plugin.Builder) ||
		!validGLIBCMax(document.Plugin.GLIBCMax) ||
		!lowerSHA256Pattern.MatchString(document.Plugin.SHA256) ||
		document.Bundle.SourceCommit != document.Plugin.SourceCommit ||
		document.Bundle.Builder != document.Plugin.Builder ||
		len(document.Files) == 0 ||
		len(document.ManifestExclusions) != 2 ||
		document.ManifestExclusions[0] != "approved-artifacts.json" ||
		document.ManifestExclusions[1] != "SHA256SUMS" {
		return false
	}
	required := map[string]string{
		"cli-proxy-api": document.CLI.BinarySHA256,
		"plugins/linux/amd64/token-saver-v1.0.2.so": document.Plugin.SHA256,
	}
	previous := ""
	seen := make(map[string]struct{}, len(document.Files))
	for _, file := range document.Files {
		if file.Path == "" || file.Path != path.Clean(file.Path) || strings.HasPrefix(file.Path, "/") ||
			file.Path == "." || strings.HasPrefix(file.Path, "../") ||
			!lowerSHA256Pattern.MatchString(file.SHA256) || !modePattern.MatchString(file.Mode) ||
			(previous != "" && file.Path <= previous) {
			return false
		}
		if _, duplicate := seen[file.Path]; duplicate {
			return false
		}
		seen[file.Path] = struct{}{}
		previous = file.Path
		if expected, ok := required[file.Path]; ok && expected != file.SHA256 {
			return false
		}
	}
	for requiredPath := range required {
		if _, ok := seen[requiredPath]; !ok {
			return false
		}
	}
	return true
}

func validGLIBCMax(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return false
	}
	version := make([]int, len(parts))
	for index, part := range parts {
		component, err := strconv.Atoi(part)
		if err != nil || component < 0 {
			return false
		}
		version[index] = component
	}
	limit := []int{2, 32}
	for index := 0; index < len(version) || index < len(limit); index++ {
		var observed, maximum int
		if index < len(version) {
			observed = version[index]
		}
		if index < len(limit) {
			maximum = limit[index]
		}
		if observed != maximum {
			return observed < maximum
		}
	}
	return true
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
