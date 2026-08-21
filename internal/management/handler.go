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

const checkCooldown = 2 * time.Second

// Options contains the process-owned dependencies needed by the management
// projection. ConfigSnapshot must return the runtime's currently published
// store; it is never serialized directly.
type Options struct {
	BuildVersion   string
	ABIVersion     uint32
	RPCSchema      uint32
	Saver         *saver.Service
	Now            func() time.Time
	ConfigSnapshot func() *config.Store
	HeadroomCheck  HeadroomCheckFunc
}

// Handler owns only redacted management state.
type Handler struct {
	buildVersion  string
	abiVersion     uint32
	rpcSchema     uint32
	saver         *saver.Service
	now            func() time.Time
	startedAt      time.Time
	configSnapshot func() *config.Store
	headroomCheck  HeadroomCheckFunc

	selfTestMu     sync.Mutex
	lastSelfTestAt *time.Time
	lastSelfTest   string

	checkMu       sync.Mutex
	checkInFlight  chan struct{}
	lastCheckedAt *time.Time
	lastLatencyMS *uint64
	lastOutcome   string
}

// NewHandler creates one process-lifetime management handler.
func NewHandler(options Options) *Handler {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.HeadroomCheck == nil {
		options.HeadroomCheck = headroom.Check
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
		headroomCheck:  options.HeadroomCheck,
		lastSelfTest:   SelfTestNever,
		checkInFlight:  make(chan struct{}, 1),
		lastOutcome:    HeadroomStatusUnknown,
	}
}

// Registration declares only authenticated Management API routes.
func (handler *Handler) Registration() Registration {
	return Registration{
		Routes: []Route{
			{Method: http.MethodGet, Path: StatusRoute},
			{Method: http.MethodPost, Path: SelfTestRoute},
			{Method: http.MethodGet, Path: DashboardRoute},
			{Method: http.MethodPost, Path: HeadroomCheckRoute},
		},
		Resources: []Route{
			{
				Path:        HeadroomPageRoute,
				Menu:        "Headroom 状态",
				Description: "查看 Headroom 被动健康状态并手动刷新",
			},
			{Path: HeadroomStatusRoute},
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
	case ManagementBasePath + DashboardRoute:
		if request.Method != http.MethodGet {
			return errorResponse(http.StatusMethodNotAllowed, ErrorMethodNotAllowed, "management method is not allowed")
		}
		return jsonResponse(http.StatusOK, handler.dashboard())
	case ManagementBasePath + HeadroomCheckRoute:
		if request.Method != http.MethodPost {
			return errorResponse(http.StatusMethodNotAllowed, ErrorMethodNotAllowed, "management method is not allowed")
		}
		return handler.check(ctx)
	case "/v0/resource/plugins/token-saver" + HeadroomPageRoute, "/v0/resource/plugins/token-saver/" + HeadroomPageRoute, HeadroomPageRoute:
		if request.Method != http.MethodGet {
			return errorResponse(http.StatusMethodNotAllowed, ErrorMethodNotAllowed, "resource method is not allowed")
		}
		return htmlResponse(http.StatusOK, headroomPageHTML)
	case "/v0/resource/plugins/token-saver" + HeadroomStatusRoute, "/v0/resource/plugins/token-saver/" + HeadroomStatusRoute, HeadroomStatusRoute:
		if request.Method != http.MethodGet {
			return errorResponse(http.StatusMethodNotAllowed, ErrorMethodNotAllowed, "resource method is not allowed")
		}
		return jsonResponse(http.StatusOK, handler.publicHeadroomStatus())
	default:
		return errorResponse(http.StatusNotFound, ErrorRouteNotFound, "management route was not found")
	}
}

func (handler *Handler) publicHeadroomStatus() HeadroomStatusDTO {
	cfg, valid, _ := handler.configuration()
	if !valid || !cfg.HeadroomEnabled {
		return HeadroomStatusDTO{
			Enabled: valid && cfg.HeadroomEnabled,
			Status:  HeadroomStatusDisabled,
			Circuit: HeadroomCircuitDisabled,
		}
	}
	status := HeadroomStatusDTO{Enabled: true, Status: HeadroomStatusUnknown, Circuit: HeadroomCircuitClosed}
	if handler.saver == nil {
		return status
	}
	snapshot := handler.saver.HeadroomSnapshot()
	status.Circuit = circuitProjection(snapshot.Circuit)
	if !snapshot.Observed {
		return status
	}
	if snapshot.Effective {
		status.Status = HeadroomStatusReady
	} else {
		status.Status = HeadroomStatusDegraded
	}
	return status
}

func (handler *Handler) dashboard() DashboardDTO {
	cfg, valid, _ := handler.configuration()
	metricSnapshot := metrics.Snapshot{StartedAt: handler.startedAt}
	if handler.saver != nil {
		if registry := handler.saver.Metrics(); registry != nil {
			metricSnapshot = registry.Snapshot()
		}
	}

	headroomEnabled := valid && cfg.HeadroomEnabled
	headroomStatus := HeadroomStatusDisabled
	headroomCircuit := HeadroomCircuitDisabled
	if headroomEnabled {
		headroomStatus = HeadroomStatusUnknown
		headroomCircuit = HeadroomCircuitClosed
		if handler.saver != nil {
			snap := handler.saver.HeadroomSnapshot()
			headroomCircuit = circuitProjection(snap.Circuit)
			if snap.Observed {
				if snap.Effective {
					headroomStatus = HeadroomStatusReady
				} else {
					headroomStatus = HeadroomStatusDegraded
				}
			}
		}
	}

	lastAt, lastLat, lastOut := handler.checkSnapshot()

	return DashboardDTO{
		StartedAt: metricSnapshot.StartedAt,
		Headroom: DashboardHeadroomDTO{
			Enabled:       headroomEnabled,
			URL:           cfg.HeadroomURL,
			Status:        headroomStatus,
			Circuit:       headroomCircuit,
			LastCheckedAt: lastAt,
			LastLatencyMS: lastLat,
			LastOutcome:   lastOut,
		},
		Stages: DashboardStagesDTO{
			RTK:      projectStage(metricSnapshot.Stages.RTK),
			Headroom: projectStage(metricSnapshot.Stages.Headroom),
			Caveman:  projectStage(metricSnapshot.Stages.Caveman),
			Ponytail: projectStage(metricSnapshot.Stages.Ponytail),
		},
	}
}

func projectStage(snap metrics.StageSnapshot) DashboardStageDTO {
	return DashboardStageDTO{
		Executed:     snap.Executed,
		Bypassed:     snap.Bypassed,
		FailOpen:     snap.FailOpen,
		Timeout:      snap.Timeout,
		Saturated:    snap.Saturated,
		InputBytes:   snap.InputBytes,
		OutputBytes:  snap.OutputBytes,
		SavedBytes:   snap.SavedBytes(),
		DurationNano: snap.DurationNano,
	}
}

func (handler *Handler) check(ctx context.Context) Response {
	cfg, valid, _ := handler.configuration()
	if !valid || !cfg.HeadroomEnabled {
		return errorResponse(http.StatusConflict, ErrorConfigInvalid, "headroom is disabled or configuration is invalid")
	}

	select {
	case handler.checkInFlight <- struct{}{}:
		defer func() { <-handler.checkInFlight }()
	default:
		return errorResponse(http.StatusTooManyRequests, ErrorRateLimited, "headroom check is already in flight")
	}

	handler.checkMu.Lock()
	now := handler.now()
	if handler.lastCheckedAt != nil && now.Sub(*handler.lastCheckedAt) < checkCooldown {
		handler.checkMu.Unlock()
		return errorResponse(http.StatusTooManyRequests, ErrorRateLimited, "headroom check cooldown active")
	}
	handler.checkMu.Unlock()

	timeout := time.Duration(cfg.HeadroomTimeoutMS) * time.Millisecond
	result := handler.headroomCheck(ctx, cfg.HeadroomURL, timeout)
	latencyMS := uint64(result.Latency.Milliseconds())
	testedAt := handler.now()

	handler.recordCheck(testedAt, latencyMS, string(result.Outcome))

	status := HeadroomStatusDegraded
	if result.Reachable {
		status = HeadroomStatusReady
	}

	return jsonResponse(http.StatusOK, HeadroomCheckDTO{
		Reachable: result.Reachable,
		Status:    status,
		Outcome:   string(result.Outcome),
		LatencyMS: latencyMS,
		TestedAt:  testedAt,
	})
}

func (handler *Handler) checkSnapshot() (*time.Time, *uint64, string) {
	handler.checkMu.Lock()
	defer handler.checkMu.Unlock()
	var at *time.Time
	if handler.lastCheckedAt != nil {
		copy := *handler.lastCheckedAt
		at = &copy
	}
	var lat *uint64
	if handler.lastLatencyMS != nil {
		copy := *handler.lastLatencyMS
		lat = &copy
	}
	return at, lat, handler.lastOutcome
}

func (handler *Handler) recordCheck(at time.Time, latencyMS uint64, outcome string) {
	handler.checkMu.Lock()
	defer handler.checkMu.Unlock()
	copyAt := at
	copyLat := latencyMS
	handler.lastCheckedAt = &copyAt
	handler.lastLatencyMS = &copyLat
	handler.lastOutcome = outcome
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
		Config:               configState,
		ConfigGeneration:   handler.generation(),
		ConfigDigest:       digest,
		Pipeline:           pipeline,
		Dependency:         dependency,
		HeadroomDesired:    cfg.HeadroomEnabled,
		HeadroomEffective:  headroomEffective,
		HeadroomCircuit:    headroomCircuit,
		Current:           metricSnapshot.Current,
		Previous:          metricSnapshot.Previous,
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
