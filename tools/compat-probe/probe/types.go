package probe

import (
	"encoding/json"
	"time"
)

const (
	SchemaVersion  = 1
	RequiredPlugin = "token-saver"
	CavemanMarker  = "[CPA_TOKEN_SAVER_CAVEMAN_START]"
)

const (
	CodeOK                  = "ok"
	CodeCandidateInvalid    = "candidate_invalid"
	CodePluginInvalid       = "plugin_invalid"
	CodePluginIdentity      = "plugin_identity_mismatch"
	CodeTemporaryState      = "temporary_state_failed"
	CodeCandidateStart      = "candidate_start_failed"
	CodeCandidateExit       = "candidate_exit"
	CodeCoreTimeout         = "core_timeout"
	CodeManagementAuth      = "management_auth_failed"
	CodePluginList          = "plugin_list_failed"
	CodePluginNotRegistered = "plugin_not_registered"
	CodePluginNotEffective  = "plugin_not_effective"
	CodeStatus              = "plugin_status_failed"
	CodePluginABI           = "plugin_abi_mismatch"
	CodePluginRPC           = "plugin_rpc_mismatch"
	CodePluginFixture       = "plugin_fixture_mismatch"
	CodeConfigGet           = "config_get_failed"
	CodeConfigPatch         = "config_patch_failed"
	CodeConfigApplyTimeout  = "config_apply_timeout"
	CodeDispatch            = "dispatch_failed"
	CodeMarkerAbsent        = "marker_absent"
	CodeMarkerDuplicated    = "marker_duplicated"
	CodeSelfTest            = "self_test_failed"
)

type Options struct {
	CandidatePath string
	PluginPath    string
	Timeout       time.Duration
}

type Report struct {
	SchemaVersion    int    `json:"schema_version"`
	Compatible       bool   `json:"compatible"`
	Code             string `json:"code"`
	PluginID         string `json:"plugin_id,omitempty"`
	PluginVersion    string `json:"plugin_version,omitempty"`
	MarkerCount      int    `json:"marker_count"`
	ConfigGeneration uint64 `json:"config_generation,omitempty"`
	ConfigDigest     string `json:"config_digest,omitempty"`
}

func (report Report) JSON() []byte {
	raw, errMarshal := json.Marshal(report)
	if errMarshal != nil {
		return []byte(`{"schema_version":1,"compatible":false,"code":"temporary_state_failed","marker_count":0}`)
	}
	return raw
}

func failure(code string) Report {
	return Report{SchemaVersion: SchemaVersion, Code: code}
}
