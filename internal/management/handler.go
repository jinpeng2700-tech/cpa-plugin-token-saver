package management

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/config"
	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/headroom"
	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/metrics"
	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/saver"
)

var selfTestBody = []byte(`{"messages":[{"role":"user","content":"Verify the local token saver pipeline."}]}`)

// Options contains the process-owned dependencies needed by the management
// projection. ConfigSnapshot must return the runtime's currently published
// store; it is never serialized directly.
type Options struct {
	BuildVersion   string
	ABIVersion     uint32
	RPCSchema      uint32
	Saver          *saver.Service
	Now            func() time.Time
	ConfigSnapshot func() *config.Store
}

// Handler owns only redacted management state.
type Handler struct {
	buildVersion   string
	abiVersion     uint32
	rpcSchema      uint32
	saver          *saver.Service
	now            func() time.Time
	startedAt      time.Time
	configSnapshot func() *config.Store

	selfTestMu     sync.Mutex
	lastSelfTestAt *time.Time
	lastSelfTest   string
}

// NewHandler creates one process-lifetime management handler.
func NewHandler(options Options) *Handler {
	if options.Now == nil {
		options.Now = time.Now
	}
	buildVersion := options.BuildVersion
	if buildVersion == "" {
		buildVersion = "unknown"
	}
	return &Handler{
		buildVersion:   buildVersion,
		abiVersion:     options.ABIVersion,
		rpcSchema:      options.RPCSchema,
		saver:          options.Saver,
		now:            options.Now,
		startedAt:      options.Now(),
		configSnapshot: options.ConfigSnapshot,
		lastSelfTest:   SelfTestNever,
	}
}

// Registration declares only authenticated Management API routes.
func (handler *Handler) Registration() Registration {
	return Registration{
		Routes: []Route{
			{Method: http.MethodGet, Path: StatusRoute},
			{Method: http.MethodPost, Path: SelfTestRoute},
		},
		Resources: []Route{
			{
				Path:        HeadroomPageRoute,
				Menu:        "Headroom 状态",
				Description: "查看 Headroom 连通状态、压缩延时与一键自检",
			},
		},
	}
}

// Handle dispatches one real-host management request and converts all panics
// to a stable, generic JSON response.
func (handler *Handler) Handle(ctx context.Context, request Request) (response Response) {
	defer func() {
		if recover() != nil {
			response = errorResponse(http.StatusInternalServerError, ErrorInternal, "management request failed")
		}
	}()
	if handler == nil {
		return errorResponse(http.StatusServiceUnavailable, ErrorUnavailable, "token saver runtime is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	switch request.Path {
	case ManagementBasePath + StatusRoute:
		if request.Method != http.MethodGet {
			return errorResponse(http.StatusMethodNotAllowed, ErrorMethodNotAllowed, "management method is not allowed")
		}
		return jsonResponse(http.StatusOK, handler.status(ctx))
	case ManagementBasePath + SelfTestRoute:
		if request.Method != http.MethodPost {
			return errorResponse(http.StatusMethodNotAllowed, ErrorMethodNotAllowed, "management method is not allowed")
		}
		return handler.selfTest(ctx)
	case "/v0/resource/plugins/token-saver" + HeadroomPageRoute, "/v0/resource/plugins/token-saver/" + HeadroomPageRoute, HeadroomPageRoute:
		if request.Method != http.MethodGet {
			return errorResponse(http.StatusMethodNotAllowed, ErrorMethodNotAllowed, "resource method is not allowed")
		}
		return htmlResponse(http.StatusOK, headroomPageHTML)
	default:
		return errorResponse(http.StatusNotFound, ErrorRouteNotFound, "management route was not found")
	}
}

func (handler *Handler) status(ctx context.Context) StatusDTO {
	cfg, valid, digest := handler.configuration()
	metricSnapshot := metrics.Snapshot{StartedAt: handler.startedAt}
	if registry := handler.saver.Metrics(); registry != nil {
		metricSnapshot = registry.Snapshot()
	}
	pipeline := PipelineAllBypassed
	if cfg.RTKEnabled || cfg.HeadroomEnabled || cfg.CavemanEnabled || cfg.PonytailEnabled {
		pipeline = PipelineActive
	}
	dependency := DependencyDisabled
	headroomCircuit := HeadroomCircuitDisabled
	headroomEffective := false
	if cfg.HeadroomEnabled {
		dependency = DependencyDegraded
		headroomCircuit = HeadroomCircuitClosed
		health := handler.saver.HeadroomStatus(ctx)
		headroomEffective = health.Effective
		headroomCircuit = circuitProjection(health.Circuit)
		if health.Effective {
			dependency = DependencyReady
		}
	}
	configState := ConfigValid
	if !valid {
		configState = ConfigError
	}
	lastAt, lastResult := handler.selfTestSnapshot()
	return StatusDTO{
		BuildVersion:       handler.buildVersion,
		ABIVersion:         handler.abiVersion,
		RPCSchema:          handler.rpcSchema,
		FixtureRevision:    FixtureRevision,
		StartedAt:          metricSnapshot.StartedAt,
		Live:               handler.saver != nil,
		Config:             configState,
		ConfigGeneration:   handler.generation(),
		ConfigDigest:       digest,
		Pipeline:           pipeline,
		Dependency:         dependency,
		HeadroomDesired:    cfg.HeadroomEnabled,
		HeadroomEffective:  headroomEffective,
		HeadroomCircuit:    headroomCircuit,
		Current:            metricSnapshot.Current,
		Previous:           metricSnapshot.Previous,
		LastSelfTestAt:     lastAt,
		LastSelfTestResult: lastResult,
		Metrics:            metricSnapshot.Stages,
	}
}

func (handler *Handler) selfTest(ctx context.Context) Response {
	if handler.saver == nil {
		handler.recordSelfTest(SelfTestFailed)
		return errorResponse(http.StatusServiceUnavailable, ErrorUnavailable, "token saver runtime is unavailable")
	}
	testedAt := handler.now()
	for attempt := 0; attempt < 2; attempt++ {
		generation := handler.saver.Generation()
		cfg, valid, _ := handler.configuration()
		if !valid {
			handler.recordSelfTestAt(testedAt, SelfTestFailed)
			return errorResponse(http.StatusConflict, ErrorConfigInvalid, "runtime configuration is invalid")
		}
		if generation != handler.saver.Generation() {
			continue
		}
		model := "self-test-model"
		if len(cfg.ModelAllowlist) > 0 {
			model = cfg.ModelAllowlist[0]
		}
		result := handler.saver.Normalize(ctx, saver.Request{
			FromFormat: "openai",
			ToFormat:   "openai",
			Model:      model,
			Body:       append([]byte(nil), selfTestBody...),
		})
		if generation != handler.saver.Generation() {
			continue
		}
		if !json.Valid(result) {
			handler.recordSelfTestAt(testedAt, SelfTestFailed)
			return errorResponse(http.StatusInternalServerError, ErrorSelfTestFailed, "local pipeline self-test failed")
		}
		handler.recordSelfTestAt(testedAt, SelfTestPassed)
		return jsonResponse(http.StatusOK, SelfTestDTO{
			FixtureRevision: FixtureRevision,
			TestedAt:        testedAt,
			Result:          SelfTestPassed,
		})
	}
	handler.recordSelfTestAt(testedAt, SelfTestFailed)
	return errorResponse(http.StatusConflict, ErrorConfigChanged, "runtime configuration changed during self-test")
}

func (handler *Handler) configuration() (config.Config, bool, string) {
	if handler.configSnapshot == nil {
		cfg := config.Defaults()
		return cfg, false, config.Digest(cfg)
	}
	return handler.configSnapshot().StatusSnapshot()
}

func (handler *Handler) generation() uint64 {
	return handler.saver.Generation()
}

func (handler *Handler) selfTestSnapshot() (*time.Time, string) {
	handler.selfTestMu.Lock()
	defer handler.selfTestMu.Unlock()
	var at *time.Time
	if handler.lastSelfTestAt != nil {
		copy := *handler.lastSelfTestAt
		at = &copy
	}
	return at, handler.lastSelfTest
}

func (handler *Handler) recordSelfTest(result string) {
	handler.recordSelfTestAt(handler.now(), result)
}

func (handler *Handler) recordSelfTestAt(at time.Time, result string) {
	handler.selfTestMu.Lock()
	defer handler.selfTestMu.Unlock()
	copy := at
	handler.lastSelfTestAt = &copy
	handler.lastSelfTest = result
}

func circuitProjection(state headroom.CircuitState) string {
	switch state {
	case headroom.CircuitOpen:
		return HeadroomCircuitOpen
	case headroom.CircuitHalfOpen:
		return HeadroomCircuitHalfOpen
	default:
		return HeadroomCircuitClosed
	}
}

func htmlResponse(statusCode int, body []byte) Response {
	headers := make(http.Header)
	headers.Set("Content-Type", "text/html; charset=utf-8")
	return Response{
		StatusCode: statusCode,
		Headers:    headers,
		Body:       body,
	}
}

func jsonResponse(statusCode int, payload any) Response {
	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return errorResponse(http.StatusInternalServerError, ErrorInternal, "management response failed")
	}
	return Response{
		StatusCode: statusCode,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       body,
	}
}

func errorResponse(statusCode int, code, message string) Response {
	body, errMarshal := json.Marshal(ErrorDTO{Error: ErrorDetail{Code: code, Message: message}})
	if errMarshal != nil {
		body = []byte(`{"error":{"code":"internal_error","message":"management response failed"}}`)
	}
	return Response{
		StatusCode: statusCode,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       body,
	}
}
