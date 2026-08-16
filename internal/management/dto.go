// Package management implements the authenticated Token Saver management RPC.
package management

import (
	"net/http"
	"time"

	"github.com/router-for-me/cpa-plugin-token-saver/internal/metrics"
)

const (
	ManagementBasePath = "/v0/management"
	StatusRoute        = "/plugins/token-saver/status"
	SelfTestRoute      = "/plugins/token-saver/self-test"
	FixtureRevision    = "v1"
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

	SelfTestNever  = "never"
	SelfTestPassed = "passed"
	SelfTestFailed = "failed"
)

const (
	ErrorRouteNotFound    = "route_not_found"
	ErrorMethodNotAllowed = "method_not_allowed"
	ErrorConfigInvalid    = "config_invalid"
	ErrorConfigChanged    = "config_changed"
	ErrorUnavailable      = "runtime_unavailable"
	ErrorSelfTestFailed   = "self_test_failed"
	ErrorInternal         = "internal_error"
)

// Route intentionally uses Go's default JSON field names because RPC schema 3
// mirrors the host SDK's Method and Path fields.
type Route struct {
	Method string
	Path   string
}

// Registration is the management.register result. Resources remains an
// explicit empty list because Token Saver exposes no unauthenticated UI route.
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
	ABIVersion         uint32                     `json:"abi_version"`
	RPCSchema          uint32                     `json:"rpc_schema"`
	FixtureRevision    string                     `json:"fixture_revision"`
	StartedAt          time.Time                  `json:"started_at"`
	Live               bool                       `json:"live"`
	Config             string                     `json:"config"`
	ConfigGeneration   uint64                     `json:"config_generation"`
	ConfigDigest       string                     `json:"config_digest"`
	Pipeline           string                     `json:"pipeline"`
	Dependency         string                     `json:"dependency"`
	HeadroomDesired    bool                       `json:"headroom_desired"`
	HeadroomEffective  bool                       `json:"headroom_effective"`
	HeadroomCircuit    string                     `json:"headroom_circuit"`
	Current            metrics.GenerationSnapshot `json:"current"`
	Previous           metrics.GenerationSnapshot `json:"previous"`
	LastSelfTestAt     *time.Time                 `json:"last_self_test_at"`
	LastSelfTestResult string                     `json:"last_self_test_result"`
	Metrics            metrics.StageProjection    `json:"metrics"`
}

// SelfTestDTO reports only the local pipeline fixture result. It deliberately
// makes no claim about host hook dispatch or any upstream LLM.
type SelfTestDTO struct {
	FixtureRevision string    `json:"fixture_revision"`
	TestedAt        time.Time `json:"tested_at"`
	Result          string    `json:"result"`
}

type ErrorDTO struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
