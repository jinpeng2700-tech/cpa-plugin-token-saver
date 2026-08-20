package verifier

const (
	ApprovalSchemaVersion = 2
	VerifierSchemaVersion = 1
	FixtureRevision       = "v1"
)

const (
	PhasePreflight   Phase = "preflight"
	PhasePostInstall Phase = "postinstall"
)

const (
	ClassificationCompatible       = "compatible"
	ClassificationBlocked          = "blocked"
	ClassificationCandidateFailure = "candidate_failure"
)

const (
	CodeOK                    = "ok"
	CodeApprovalRead          = "approval_read_failed"
	CodeApprovalUntrusted     = "approval_untrusted"
	CodeApprovalInvalid       = "approval_invalid"
	CodeApprovalSchema        = "approval_schema_mismatch"
	CodeVerifierSchema        = "verifier_schema_mismatch"
	CodeApprovalABI           = "approval_abi_mismatch"
	CodeApprovalRPC           = "approval_rpc_mismatch"
	CodeCredentialDirectory   = "credential_directory_missing"
	CodeCredentialRead        = "credential_read_failed"
	CodeCredentialInvalid     = "credential_invalid"
	CodeArchitectureMismatch  = "architecture_mismatch"
	CodeCLIHashMismatch       = "cli_hash_mismatch"
	CodePluginHashMismatch    = "plugin_hash_mismatch"
	CodeCoreUnavailable       = "core_unavailable"
	CodeManagementAuth        = "management_auth_failed"
	CodeManagementUnavailable = "management_unavailable"
	CodeManagementURL         = "management_url_invalid"
	CodePluginMissing         = "plugin_missing"
	CodePluginNotRegistered   = "plugin_not_registered"
	CodePluginNotEffective    = "plugin_not_effective"
	CodePluginVersionMismatch = "plugin_version_mismatch"
	CodeABIMismatch           = "abi_mismatch"
	CodeRPCMismatch           = "rpc_mismatch"
	CodeFixtureMismatch       = "fixture_mismatch"
	CodeConfigInvalid         = "config_invalid"
	CodeConfigRace            = "config_race"
	CodeConfigDigestMismatch  = "config_digest_mismatch"
	CodeRuntimeUnhealthy      = "runtime_unhealthy"
	CodeSelfTestFailed        = "self_test_failed"
	CodeArtifactRead          = "artifact_read_failed"
)

type Phase string

type Result struct {
	SchemaVersion  int    `json:"schema_version"`
	Compatible     bool   `json:"compatible"`
	Classification string `json:"classification"`
	Code           string `json:"code"`
}

type Approval struct {
	SchemaVersion  int            `json:"schema_version"`
	VerifierSchema int            `json:"verifier_schema"`
	CLI            ApprovedCLI    `json:"cli"`
	Plugin         ApprovedPlugin `json:"plugin"`
}

type ApprovedCLI struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Arch    string `json:"arch"`
}

type ApprovedPlugin struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	ABI     uint32 `json:"abi"`
	RPC     uint32 `json:"rpc"`
}

type Artifacts struct {
	Arch       string
	CLIHash    string
	PluginHash string
}

type PluginState struct {
	Found            bool
	Registered       bool
	EffectiveEnabled bool
	Version          string
}

type Status struct {
	BuildVersion      string `json:"build_version"`
	ABIVersion        uint32 `json:"abi_version"`
	RPCSchema         uint32 `json:"rpc_schema"`
	FixtureRevision   string `json:"fixture_revision"`
	Live              bool   `json:"live"`
	Config            string `json:"config"`
	ConfigGeneration  uint64 `json:"config_generation"`
	ConfigDigest      string `json:"config_digest"`
	Pipeline          string `json:"pipeline"`
	Dependency        string `json:"dependency"`
	HeadroomDesired   bool   `json:"headroom_desired"`
	HeadroomEffective bool   `json:"headroom_effective"`
}

type SelfTest struct {
	FixtureRevision string `json:"fixture_revision"`
	Result          string `json:"result"`
}

type RuntimeObservation struct {
	Plugin       PluginState
	Before       Status
	After        Status
	ConfigDigest string
	SelfTest     SelfTest
}

type Options struct {
	BaseURL    string
	Credential string
	Approval   Approval
	Phase      Phase
	CLIPath    string
	PluginPath string
}

func compatible() Result {
	return Result{SchemaVersion: VerifierSchemaVersion, Compatible: true, Classification: ClassificationCompatible, Code: CodeOK}
}

func blocked(code string) Result {
	return Result{SchemaVersion: VerifierSchemaVersion, Classification: ClassificationBlocked, Code: code}
}

func phaseFailure(phase Phase, code string) Result {
	if phase == PhasePostInstall {
		return Result{SchemaVersion: VerifierSchemaVersion, Classification: ClassificationCandidateFailure, Code: code}
	}
	return blocked(code)
}
