// Package management implements the authenticated Token Saver management RPC.
package management

import (
	"context"
	"net/http"
	"time"

	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/headroom"
	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/metrics"
)

const (
	ManagementBasePath  = "/v0/management"
	StatusRoute         = "/plugins/token-saver/status"
	SelfTestRoute       = "/plugins/token-saver/self-test"
	DashboardRoute      = "/plugins/token-saver/dashboard"
	HeadroomCheckRoute  = "/plugins/token-saver/headroom/check"
	HeadroomPageRoute   = "/headroom"
	HeadroomStatusRoute = "/headroom/status"
	FixtureRevision     = "v1"
)

const (
	ConfigValid = "valid"
	ConfigError = "config_error"

	PipelineAllBypassed = "all_bypassed"
	PipelineActive      = "active"

	DependencyDisabled = "disabled"
	DependencyReady    = "ready"
	DependencyDegraded = "degraded"

	HeadroomCircuitDisabled = "disabled"
	HeadroomCircuitClosed   = "closed"
	HeadroomCircuitOpen     = "open"
	HeadroomCircuitHalfOpen = "half_open"

	HeadroomStatusDisabled = "disabled"
	HeadroomStatusUnknown  = "unknown"
	HeadroomStatusReady    = "ready"
	HeadroomStatusDegraded = "degraded"

	SelfTestNever  = "never"
	SelfTestPassed = "passed"
	SelfTestFailed = "failed"
)

const (
	ErrorRouteNotFound    = "route_not_found"
	ErrorMethodNotAllowed = "method_not_allowed"
	ErrorConfigInvalid    = "config_invalid"
	ErrorRateLimited      = "rate_limited"
	ErrorConfigChanged    = "config_changed"
	ErrorUnavailable      = "runtime_unavailable"
	ErrorSelfTestFailed   = "self_test_failed"
	ErrorInternal         = "internal_error"
)

// Route intentionally uses Go's default JSON field names because RPC schema 3
// mirrors the host SDK's Method and Path fields.
type Route struct {
	Method      string
	Path        string
	Menu        string
	Description string
}

// Registration is the management.register result.
type Registration struct {
	Routes    []Route `json:"routes"`
	Resources []Route `json:"resources"`
}

// Request decodes only the host fields this plugin needs. Sensitive headers,
// query values, and body bytes are ignored by encoding/json.
type Request struct {
	Method string
	Path   string
}

// Response mirrors the real host ManagementResponse JSON.
type Response struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// StatusDTO is the fixed, redacted projection consumed by the UI and updater.
type StatusDTO struct {
	BuildVersion       string                     `json:"build_version"`
	ABIVersion         uint32                       `json:"abi_version"`
	RPCSchema          uint32                      `json:"rpc_schema"`
	FixtureRevision    string                     `json:"fixture_revision"`
	StartedAt          time.Time                  `json:"started_at"`
	Live               bool                       `json:"live"`
	Config               string                     `json:"config"`
	ConfigGeneration   uint64                      `json:"config_generation"`
	ConfigDigest       string                     `json:"config_digest"`
	Pipeline           string                     `json:"pipeline"`
	Dependency         string                     `json:"dependency"`
	HeadroomDesired    bool                       `json:"headroom_desired"`
	HeadroomEffective  bool                       `json:"headroom_effective"`
	HeadroomCircuit    string                     `json:"headroom_circuit"`
	Current           metrics.GenerationSnapshot `json:"current"`
	Previous          metrics.GenerationSnapshot `json:"previous"`
	LastSelfTestAt     *time.Time                 `json:"last_self_test_at"`
	LastSelfTestResult string                     `json:"last_self_test_result"`
	Metrics            metrics.StageProjection    `json:"metrics"`
}

// HeadroomStatusDTO is the public, read-only projection
// used by the embedded dashboard. It excludes configuration identity
// and authenticated management metadata.
type HeadroomStatusDTO struct {
	Enabled bool   `json:"enabled"`
	Status  string `json:"status"`
	Circuit string `json:"circuit"`
}

// SelfTestDTO reports only the local pipeline fixture result. It
// deliberately makes no claim about host hook dispatch or any upstream LLM.
type SelfTestDTO struct {
	FixtureRevision string    `json:"fixture_revision"`
	TestedAt        time.Time `json:"tested_at"`
	Result          string    `json:"result"`
}

// DashboardStageDTO projects fixed stage counters with safe saved-byte projection.
type DashboardStageDTO struct {
	Executed     uint64 `json:"executed"`
	Bypassed     uint64 `json:"bypassed"`
	FailOpen     uint64 `json:"fail_open"`
	Timeout      uint64 `json:"timeout"`
	Saturated    uint64 `json:"saturated"`
	InputBytes   uint64 `json:"input_bytes"`
	OutputBytes  uint64 `json:"output_bytes"`
	SavedBytes   uint64 `json:"saved_bytes"`
	DurationNano uint64 `json:"duration_ns"`
}

type DashboardStagesDTO struct {
	RTK      DashboardStageDTO `json:"rtk"`
	Headroom DashboardStageDTO `json:"headroom"`
	Caveman  DashboardStageDTO `json:"caveman"`
	Ponytail DashboardStageDTO `json:"ponytail"`
}

type DashboardHeadroomDTO struct {
	Enabled       bool       `json:"enabled"`
	URL           string     `json:"url"`
	Status        string     `json:"status"`
	Circuit       string     `json:"circuit"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
	LastLatencyMS *uint64    `json:"last_latency_ms"`
	LastOutcome   string     `json:"last_outcome"`
}

type DashboardDTO struct {
	StartedAt time.Time            `json:"started_at"`
	Headroom  DashboardHeadroomDTO `json:"headroom"`
	Stages    DashboardStagesDTO   `json:"stages"`
}

type HeadroomCheckDTO struct {
	Reachable bool      `json:"reachable"`
	Status    string    `json:"status"`
	Outcome   string    `json:"outcome"`
	LatencyMS uint64    `json:"latency_ms"`
	TestedAt  time.Time `json:"tested_at"`
}

type HeadroomCheckFunc func(context.Context, string, time.Duration) headroom.CheckResult

type ErrorDTO struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
