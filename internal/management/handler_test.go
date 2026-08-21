package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/config"
	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/headroom"
	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/metrics"
	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/saver"
)

func TestRegistrationDeclaresAuthenticatedRoutesAndPublicHeadroomResources(t *testing.T) {
	handler := NewHandler(Options{})
	registration := handler.Registration()
	wantRoutes := []Route{
		{Method: http.MethodGet, Path: StatusRoute},
		{Method: http.MethodPost, Path: SelfTestRoute},
		{Method: http.MethodGet, Path: DashboardRoute},
		{Method: http.MethodPost, Path: HeadroomCheckRoute},
	}
	if !reflect.DeepEqual(registration.Routes, wantRoutes) {
		t.Fatalf("routes = %#v, want %#v", registration.Routes, wantRoutes)
	}
	wantResources := []Route{
		{
			Path:        HeadroomPageRoute,
			Menu:        "Headroom 状态",
			Description: "查看 Headroom 被动健康状态并手动刷新",
		},
		{Path: "/headroom/status"},
	}
	if !reflect.DeepEqual(registration.Resources, wantResources) {
		t.Fatalf("resources = %#v, want %#v", registration.Resources, wantResources)
	}
	raw, errMarshal := json.Marshal(registration)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	for _, want := range []string{`"routes"`, `"resources"`, `"Method":"GET"`, `"Path":"/plugins/token-saver/status"`} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("registration %s missing %q", raw, want)
		}
	}
	for _, forbidden := range []string{`"method"`, `"path"`, `"menu"`, `"Handler"`} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Errorf("registration %s contains forbidden field %q", raw, forbidden)
		}
	}
}

func TestPublicHeadroomStatusReturnsOnlyDashboardProjection(t *testing.T) {
	runner := &statusRunner{probe: headroom.OutcomeApplied, circuit: headroom.CircuitClosed}
	service := saver.NewService(saver.Options{
		HeadroomFactory: func(config.Config) (saver.HeadroomRunner, func(), error) { return runner, func() {}, nil },
	})
	defer service.Close()
	cfg := config.Defaults()
	cfg.HeadroomEnabled = true
	if err := service.Reconfigure(cfg); err != nil {
		t.Fatal(err)
	}
	store, errStore := config.NewStore([]byte("future_credential: TOP_SECRET_SENTINEL\nheadroom_enabled: true\n"))
	if errStore != nil {
		t.Fatal(errStore)
	}
	handler := NewHandler(Options{
		BuildVersion:   "test-build",
		Saver:          service,
		ConfigSnapshot: func() *config.Store { return store },
	})

	response := handler.Handle(context.Background(), Request{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/token-saver/headroom/status",
	})
	if response.StatusCode != http.StatusOK || response.Headers.Get("Content-Type") != "application/json" {
		t.Fatalf("response = %#v", response)
	}
	var fields map[string]json.RawMessage
	if errDecode := json.Unmarshal(response.Body, &fields); errDecode != nil {
		t.Fatalf("decode public status: %v; body=%s", errDecode, response.Body)
	}
	wantFields := []string{"enabled", "status", "circuit"}
	if len(fields) != len(wantFields) {
		t.Fatalf("public status fields = %#v, want exactly %v", fields, wantFields)
	}
	for _, field := range wantFields {
		if _, exists := fields[field]; !exists {
			t.Errorf("public status missing field %q", field)
		}
	}
	for _, forbidden := range []string{
		"TOP_SECRET_SENTINEL", "config_digest", "config_generation", "started_at",
		"abi_version", "rpc_schema", "fixture_revision", "build_version",
		"last_self_test", "metrics", "http://127.0.0.1:8787",
	} {
		if bytes.Contains(response.Body, []byte(forbidden)) {
			t.Errorf("public status leaked %q: %s", forbidden, response.Body)
		}
	}
	if runner.probes.Load() != 0 {
		t.Fatalf("public status performed %d active probes, want 0", runner.probes.Load())
	}
}

func TestHeadroomPageNeverRequestsAuthenticatedManagementRoutes(t *testing.T) {
	handler := NewHandler(Options{})
	response := handler.Handle(context.Background(), Request{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/token-saver/headroom",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("page response = %d %s", response.StatusCode, response.Body)
	}
	body := string(response.Body)
	if !strings.Contains(body, "/v0/resource/plugins/token-saver/headroom/status") {
		t.Fatalf("page does not use public status resource: %s", response.Body)
	}
	for _, required := range []string{
		"cpa.plugin.management.hello",
		"cpa.plugin.management.ready",
		"cpa.plugin.management.request",
		"cpa.plugin.management.response",
		"/plugins/token-saver/dashboard",
		"/plugins/token-saver/headroom/check",
		"需要新版管理面板",
		"saved_bytes",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("page missing required string %q", required)
		}
	}
	for _, forbidden := range []string{
		"fetch('/v0/management", "fetch(\"/v0/management", "fetch(`/v0/management",
		"managementKey", "Authorization", "localStorage", "sessionStorage",
		"URLSearchParams", "document.cookie",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("page contains forbidden authenticated flow %q", forbidden)
		}
	}
}

func TestStatusAllOffIsHealthyAndRedacted(t *testing.T) {
	now := time.Date(2026, time.August, 17, 3, 4, 5, 0, time.UTC)
	service := saver.NewService(saver.Options{Now: func() time.Time { return now }})
	defer service.Close()
	if err := service.Reconfigure(config.Defaults()); err != nil {
		t.Fatal(err)
	}
	store, errStore := config.NewStore([]byte("future_credential: TOP_SECRET_SENTINEL\nheadroom_url: http://127.0.0.1:8787\n"))
	if errStore != nil {
		t.Fatal(errStore)
	}
	handler := NewHandler(Options{
		BuildVersion: "test-build", ABIVersion: 1, RPCSchema: 3, Saver: service,
		Now:            func() time.Time { return now },
		ConfigSnapshot: func() *config.Store { return store },
	})
	response := handler.Handle(context.Background(), Request{Method: http.MethodGet, Path: ManagementBasePath + StatusRoute})
	if response.StatusCode != http.StatusOK || response.Headers.Get("Content-Type") != "application/json" {
		t.Fatalf("response = %#v", response)
	}
	status := decodeStatus(t, response)
	if status.BuildVersion != "test-build" || status.ABIVersion != 1 || status.RPCSchema != 3 || status.FixtureRevision != FixtureRevision {
		t.Fatalf("identity projection = %#v", status)
	}
	if !status.Live || status.Config != ConfigValid || status.ConfigGeneration != 1 || status.ConfigDigest != config.Digest(config.Defaults()) {
		t.Fatalf("runtime projection = %#v", status)
	}
	if status.Pipeline != PipelineAllBypassed || status.Dependency != DependencyDisabled || status.HeadroomDesired || status.HeadroomEffective || status.HeadroomCircuit != HeadroomCircuitDisabled {
		t.Fatalf("all-off projection = %#v", status)
	}
	if status.Current.Generation != 1 || status.Previous.Generation != 0 || status.LastSelfTestResult != SelfTestNever || status.LastSelfTestAt != nil {
		t.Fatalf("generation/self-test projection = %#v", status)
	}
	for _, forbidden := range []string{"TOP_SECRET_SENTINEL", "http://127.0.0.1:8787", "headroom_url", "rtk_enabled", "Authorization", "CPA_TOKEN_SAVER_CAVEMAN_START"} {
		if bytes.Contains(response.Body, []byte(forbidden)) {
			t.Errorf("status leaked %q: %s", forbidden, response.Body)
		}
	}
	var fields map[string]json.RawMessage
	if errDecode := json.Unmarshal(response.Body, &fields); errDecode != nil {
		t.Fatal(errDecode)
	}
	wantFields := []string{
		"build_version", "abi_version", "rpc_schema", "fixture_revision", "started_at", "live", "config",
		"config_generation", "config_digest", "pipeline", "dependency", "headroom_desired", "headroom_effective",
		"headroom_circuit", "current", "previous", "last_self_test_at", "last_self_test_result", "metrics",
	}
	if len(fields) != len(wantFields) {
		t.Fatalf("status fields = %#v, want exactly %v", fields, wantFields)
	}
	for _, field := range wantFields {
		if _, exists := fields[field]; !exists {
			t.Errorf("status missing fixed field %q", field)
		}
	}
}

func TestStatusReportsHeadroomFailureAsDegraded(t *testing.T) {
	now := time.Date(2026, time.August, 17, 4, 0, 0, 0, time.UTC)
	runner := &statusRunner{probe: headroom.OutcomeConnection, circuit: headroom.CircuitClosed}
	service := saver.NewService(saver.Options{
		Now:             func() time.Time { return now },
		HeadroomFactory: func(config.Config) (saver.HeadroomRunner, func(), error) { return runner, func() {}, nil },
	})
	defer service.Close()
	cfg := config.Defaults()
	cfg.HeadroomEnabled = true
	if err := service.Reconfigure(cfg); err != nil {
		t.Fatal(err)
	}
	store, errStore := config.NewStore([]byte("headroom_enabled: true\n"))
	if errStore != nil {
		t.Fatal(errStore)
	}
	handler := NewHandler(Options{Saver: service, Now: func() time.Time { return now }, ConfigSnapshot: func() *config.Store { return store }})
	status := decodeStatus(t, handler.Handle(context.Background(), Request{Method: http.MethodGet, Path: ManagementBasePath + StatusRoute}))
	if status.Pipeline != PipelineActive || status.Dependency != DependencyDegraded || !status.HeadroomDesired || status.HeadroomEffective || status.HeadroomCircuit != HeadroomCircuitClosed {
		t.Fatalf("degraded status = %#v", status)
	}
	if runner.probes.Load() != 1 {
		t.Fatalf("probe calls = %d, want 1", runner.probes.Load())
	}
}

func TestStatusTreatsOpenCircuitAsDegradedDespiteSuccessfulCachedProbe(t *testing.T) {
	runner := &statusRunner{probe: headroom.OutcomeApplied, circuit: headroom.CircuitOpen}
	service := saver.NewService(saver.Options{
		HeadroomFactory: func(config.Config) (saver.HeadroomRunner, func(), error) { return runner, func() {}, nil },
	})
	defer service.Close()
	cfg := config.Defaults()
	cfg.HeadroomEnabled = true
	if err := service.Reconfigure(cfg); err != nil {
		t.Fatal(err)
	}
	store, errStore := config.NewStore([]byte("headroom_enabled: true\n"))
	if errStore != nil {
		t.Fatal(errStore)
	}
	handler := NewHandler(Options{Saver: service, ConfigSnapshot: func() *config.Store { return store }})
	status := decodeStatus(t, handler.Handle(context.Background(), Request{Method: http.MethodGet, Path: ManagementBasePath + StatusRoute}))
	if status.Dependency != DependencyDegraded || status.HeadroomEffective || status.HeadroomCircuit != HeadroomCircuitOpen {
		t.Fatalf("open-circuit status = %#v", status)
	}
}

func TestSelfTestUsesEnabledPipelineWithoutChangingConfiguration(t *testing.T) {
	now := time.Date(2026, time.August, 17, 5, 0, 0, 0, time.UTC)
	var calls [4]atomic.Int32
	local := func(index int) saver.StageFunc {
		return func(_ context.Context, body []byte, _ saver.Request, _ config.Config) ([]byte, error) {
			calls[index].Add(1)
			return body, nil
		}
	}
	runner := &statusRunner{
		probe: headroom.OutcomeApplied, circuit: headroom.CircuitClosed,
		apply: func(_ context.Context, body []byte, _ saver.Request) ([]byte, headroom.Outcome) {
			calls[1].Add(1)
			return body, headroom.OutcomeNoChange
		},
	}
	service := saver.NewService(saver.Options{
		Now: func() time.Time { return now }, RTK: local(0), Caveman: local(2), Ponytail: local(3),
		HeadroomFactory: func(config.Config) (saver.HeadroomRunner, func(), error) { return runner, func() {}, nil },
	})
	defer service.Close()
	rawConfig := []byte("rtk_enabled: true\nheadroom_enabled: true\ncaveman_enabled: true\ncaveman_level: lite\nponytail_enabled: true\nponytail_level: lite\nmodel_allowlist: [production-model]\n")
	store, errStore := config.NewStore(rawConfig)
	if errStore != nil {
		t.Fatal(errStore)
	}
	if err := service.Reconfigure(store.Snapshot()); err != nil {
		t.Fatal(err)
	}
	wantGeneration, wantDigest, wantConfig := service.Generation(), config.Digest(store.Snapshot()), store.Snapshot()
	handler := NewHandler(Options{Saver: service, Now: func() time.Time { return now }, ConfigSnapshot: func() *config.Store { return store }})
	response := handler.Handle(context.Background(), Request{Method: http.MethodPost, Path: ManagementBasePath + SelfTestRoute})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("self-test response = %d %s", response.StatusCode, response.Body)
	}
	var result SelfTestDTO
	if errDecode := json.Unmarshal(response.Body, &result); errDecode != nil {
		t.Fatal(errDecode)
	}
	if result.Result != SelfTestPassed || result.FixtureRevision != FixtureRevision || !result.TestedAt.Equal(now) {
		t.Fatalf("self-test result = %#v", result)
	}
	for index := range calls {
		if got := calls[index].Load(); got != 1 {
			t.Errorf("stage %d calls = %d, want 1", index, got)
		}
	}
	if service.Generation() != wantGeneration || config.Digest(store.Snapshot()) != wantDigest || !reflect.DeepEqual(store.Snapshot(), wantConfig) {
		t.Fatal("self-test changed runtime configuration")
	}
	status := decodeStatus(t, handler.Handle(context.Background(), Request{Method: http.MethodGet, Path: ManagementBasePath + StatusRoute}))
	if status.LastSelfTestResult != SelfTestPassed || status.LastSelfTestAt == nil || !status.LastSelfTestAt.Equal(now) {
		t.Fatalf("last self-test projection = %#v", status)
	}
}

func TestSelfTestKeepsHeadroomDownAsDegradedFailOpen(t *testing.T) {
	runner := &statusRunner{
		probe:   headroom.OutcomeConnection,
		circuit: headroom.CircuitClosed,
		apply: func(_ context.Context, body []byte, _ saver.Request) ([]byte, headroom.Outcome) {
			return body, headroom.OutcomeConnection
		},
	}
	service := saver.NewService(saver.Options{
		HeadroomFactory: func(config.Config) (saver.HeadroomRunner, func(), error) { return runner, func() {}, nil },
	})
	defer service.Close()
	store, errStore := config.NewStore([]byte("headroom_enabled: true\n"))
	if errStore != nil {
		t.Fatal(errStore)
	}
	if err := service.Reconfigure(store.Snapshot()); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Options{Saver: service, ConfigSnapshot: func() *config.Store { return store }})
	response := handler.Handle(context.Background(), Request{Method: http.MethodPost, Path: ManagementBasePath + SelfTestRoute})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Headroom-down self-test = %d %s", response.StatusCode, response.Body)
	}
	status := decodeStatus(t, handler.Handle(context.Background(), Request{Method: http.MethodGet, Path: ManagementBasePath + StatusRoute}))
	if status.LastSelfTestResult != SelfTestPassed || status.Dependency != DependencyDegraded || status.HeadroomEffective {
		t.Fatalf("Headroom-down projection = %#v", status)
	}
}

func TestStatusProjectsCurrentAndPreviousInflightGenerations(t *testing.T) {
	service := saver.NewService(saver.Options{})
	defer service.Close()
	if err := service.Reconfigure(config.Defaults()); err != nil {
		t.Fatal(err)
	}
	endPrevious := service.Metrics().Begin(1)
	defer endPrevious()
	if err := service.Reconfigure(config.Defaults()); err != nil {
		t.Fatal(err)
	}
	store, errStore := config.NewStore(nil)
	if errStore != nil {
		t.Fatal(errStore)
	}
	handler := NewHandler(Options{Saver: service, ConfigSnapshot: func() *config.Store { return store }})
	status := decodeStatus(t, handler.Handle(context.Background(), Request{Method: http.MethodGet, Path: ManagementBasePath + StatusRoute}))
	if status.Current.Generation != 2 || status.Current.InFlight != 0 || status.Previous.Generation != 1 || status.Previous.InFlight != 1 {
		t.Fatalf("in-flight generation projection = current:%#v previous:%#v", status.Current, status.Previous)
	}
}

func TestHandlerReturnsOnlyStableErrorsForBadDispatchAndPanic(t *testing.T) {
	handler := NewHandler(Options{})
	tests := []struct {
		name   string
		req    Request
		status int
		code   string
	}{
		{name: "unknown path", req: Request{Method: http.MethodGet, Path: ManagementBasePath + "/plugins/token-saver/missing"}, status: http.StatusNotFound, code: ErrorRouteNotFound},
		{name: "wrong method", req: Request{Method: http.MethodPost, Path: ManagementBasePath + StatusRoute}, status: http.StatusMethodNotAllowed, code: ErrorMethodNotAllowed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := handler.Handle(context.Background(), test.req)
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}
			var body ErrorDTO
			if errDecode := json.Unmarshal(response.Body, &body); errDecode != nil || body.Error.Code != test.code {
				t.Fatalf("error body = %s, decode=%v", response.Body, errDecode)
			}
		})
	}
	panicHandler := NewHandler(Options{ConfigSnapshot: func() *config.Store { panic("TOP_SECRET_RAW_PANIC") }})
	response := panicHandler.Handle(context.Background(), Request{Method: http.MethodGet, Path: ManagementBasePath + StatusRoute})
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("panic status = %d", response.StatusCode)
	}
	var body ErrorDTO
	if errDecode := json.Unmarshal(response.Body, &body); errDecode != nil || body.Error.Code != ErrorInternal {
		t.Fatalf("panic body = %s, decode=%v", response.Body, errDecode)
	}
	if bytes.Contains(response.Body, []byte("TOP_SECRET_RAW_PANIC")) {
		t.Fatalf("panic detail leaked: %s", response.Body)
	}
}

func TestStatusConfigErrorUsesSafeOffDigest(t *testing.T) {
	service := saver.NewService(saver.Options{})
	defer service.Close()
	store, errStore := config.NewStore([]byte("headroom_url: http://TOP_SECRET_INVALID.example\n"))
	if errStore == nil {
		t.Fatal("invalid cold configuration was accepted")
	}
	if err := service.Reconfigure(store.Snapshot()); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Options{Saver: service, ConfigSnapshot: func() *config.Store { return store }})
	response := handler.Handle(context.Background(), Request{Method: http.MethodGet, Path: ManagementBasePath + StatusRoute})
	status := decodeStatus(t, response)
	if status.Config != ConfigError || status.ConfigDigest != config.Digest(config.Defaults()) || status.ConfigGeneration != 1 || status.Pipeline != PipelineAllBypassed {
		t.Fatalf("cold-invalid status = %#v", status)
	}
	if strings.Contains(string(response.Body), "TOP_SECRET_INVALID") {
		t.Fatalf("invalid configuration leaked: %s", response.Body)
	}
}

type statusRunner struct {
	apply   func(context.Context, []byte, saver.Request) ([]byte, headroom.Outcome)
	probe   headroom.Outcome
	circuit headroom.CircuitState
	probes  atomic.Int32
}

func (runner *statusRunner) Apply(ctx context.Context, body []byte, request saver.Request) ([]byte, headroom.Outcome) {
	if runner.apply == nil {
		return body, headroom.OutcomeNoChange
	}
	return runner.apply(ctx, body, request)
}

func (runner *statusRunner) Probe(context.Context) headroom.Outcome {
	runner.probes.Add(1)
	return runner.probe
}
func (runner *statusRunner) CircuitState() headroom.CircuitState { return runner.circuit }
func (runner *statusRunner) LastOutcome() (headroom.Outcome, bool) {
	return runner.probe, runner.probe != ""
}

func decodeStatus(t *testing.T, response Response) StatusDTO {
	t.Helper()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status response = %d %s", response.StatusCode, response.Body)
	}
	var status StatusDTO
	if errDecode := json.Unmarshal(response.Body, &status); errDecode != nil {
		t.Fatalf("decode status: %v; body=%s", errDecode, response.Body)
	}
	return status
}

func TestDashboardReturnsPassiveStageMetricsAndHeadroomStatus(t *testing.T) {
	now := time.Date(2026, time.August, 21, 10, 0, 0, 0, time.UTC)
	var checkerCalls atomic.Int32
	service := saver.NewService(saver.Options{
		Now: func() time.Time { return now },
	})
	defer service.Close()
	service.Metrics().Record(metrics.StageRTK, metrics.OutcomeExecuted, 100, 60, 5*time.Millisecond)

	store, errStore := config.NewStore([]byte("headroom_enabled: true\nheadroom_url: http://127.0.0.1:8787\n"))
	if errStore != nil {
		t.Fatal(errStore)
	}

	handler := NewHandler(Options{
		Saver:          service,
		Now:            func() time.Time { return now },
		ConfigSnapshot: func() *config.Store { return store },
		HeadroomCheck: func(ctx context.Context, url string, timeout time.Duration) headroom.CheckResult {
			checkerCalls.Add(1)
			return headroom.CheckResult{Reachable: true, Outcome: headroom.OutcomeApplied, Latency: 10 * time.Millisecond}
		},
	})

	// Call dashboard twice
	resp1 := handler.Handle(context.Background(), Request{Method: http.MethodGet, Path: ManagementBasePath + DashboardRoute})
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("dashboard resp1 status = %d, body = %s", resp1.StatusCode, resp1.Body)
	}
	resp2 := handler.Handle(context.Background(), Request{Method: http.MethodGet, Path: ManagementBasePath + DashboardRoute})
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("dashboard resp2 status = %d, body = %s", resp2.StatusCode, resp2.Body)
	}

	if checkerCalls.Load() != 0 {
		t.Fatal("passive dashboard called Headroom")
	}

	var dashboard DashboardDTO
	if errDecode := json.Unmarshal(resp2.Body, &dashboard); errDecode != nil {
		t.Fatalf("decode dashboard: %v; body = %s", errDecode, resp2.Body)
	}

	if dashboard.Stages.RTK.SavedBytes != 40 {
		t.Fatalf("RTK = %#v", dashboard.Stages.RTK)
	}
	if dashboard.Stages.RTK.Executed != 1 || dashboard.Stages.RTK.InputBytes != 100 || dashboard.Stages.RTK.OutputBytes != 60 {
		t.Fatalf("RTK stage counters = %#v", dashboard.Stages.RTK)
	}
	if !dashboard.Headroom.Enabled || dashboard.Headroom.URL != "http://127.0.0.1:8787" || dashboard.Headroom.Status != HeadroomStatusUnknown || dashboard.Headroom.Circuit != HeadroomCircuitClosed {
		t.Fatalf("headroom initial dashboard = %#v", dashboard.Headroom)
	}
	if dashboard.Headroom.LastCheckedAt != nil || dashboard.Headroom.LastLatencyMS != nil || dashboard.Headroom.LastOutcome != "unknown" {
		t.Fatalf("headroom initial sample pointers = %#v", dashboard.Headroom)
	}
}

func TestHeadroomCheckIsolatedExecutionAndDashboardReflection(t *testing.T) {
	now := time.Date(2026, time.August, 21, 10, 0, 0, 0, time.UTC)
	var checkerCalls atomic.Int32
	var capturedURL string
	var capturedTimeout time.Duration

	service := saver.NewService(saver.Options{
		Now: func() time.Time { return now },
	})
	defer service.Close()
	service.Metrics().Record(metrics.StageRTK, metrics.OutcomeExecuted, 100, 60, 5*time.Millisecond)

	store, errStore := config.NewStore([]byte("headroom_enabled: true\nheadroom_url: http://127.0.0.1:8787\nheadroom_timeout_ms: 750\n"))
	if errStore != nil {
		t.Fatal(errStore)
	}

	checker := func(ctx context.Context, u string, timeout time.Duration) headroom.CheckResult {
		checkerCalls.Add(1)
		capturedURL = u
		capturedTimeout = timeout
		return headroom.CheckResult{
			Reachable: true,
			Outcome:   headroom.OutcomeApplied,
			Latency:   12 * time.Millisecond,
		}
	}

	handler := NewHandler(Options{
		Saver:          service,
		Now:            func() time.Time { return now },
		ConfigSnapshot: func() *config.Store { return store },
		HeadroomCheck:  checker,
	})

	metricsBefore := service.Metrics().Snapshot()
	circuitBefore := service.HeadroomSnapshot().Circuit

	resp := handler.Handle(context.Background(), Request{Method: http.MethodPost, Path: ManagementBasePath + HeadroomCheckRoute})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("check status = %d, body = %s", resp.StatusCode, resp.Body)
	}

	if capturedURL != "http://127.0.0.1:8787" || capturedTimeout != 750*time.Millisecond {
		t.Fatalf("checker args = url:%s timeout:%s", capturedURL, capturedTimeout)
	}

	var checkRes HeadroomCheckDTO
	if errDecode := json.Unmarshal(resp.Body, &checkRes); errDecode != nil {
		t.Fatalf("decode check: %v; body=%s", errDecode, resp.Body)
	}

	if !checkRes.Reachable || checkRes.Status != HeadroomStatusReady || checkRes.Outcome != string(headroom.OutcomeApplied) || checkRes.LatencyMS != 12 || !checkRes.TestedAt.Equal(now) {
		t.Fatalf("check result = %#v", checkRes)
	}

	metricsAfter := service.Metrics().Snapshot()
	circuitAfter := service.HeadroomSnapshot().Circuit
	if !reflect.DeepEqual(metricsBefore.Stages, metricsAfter.Stages) {
		t.Fatalf("metrics mutated by check: before=%#v after=%#v", metricsBefore.Stages, metricsAfter.Stages)
	}
	if circuitBefore != circuitAfter {
		t.Fatalf("circuit mutated by check: before=%v after=%v", circuitBefore, circuitAfter)
	}

	// Verify dashboard remembers the result
	respDash := handler.Handle(context.Background(), Request{Method: http.MethodGet, Path: ManagementBasePath + DashboardRoute})
	var dash DashboardDTO
	if errDecode := json.Unmarshal(respDash.Body, &dash); errDecode != nil {
		t.Fatalf("decode dashboard: %v", errDecode)
	}
	if dash.Headroom.LastLatencyMS == nil || *dash.Headroom.LastLatencyMS != 12 {
		t.Fatalf("dash LastLatencyMS = %#v", dash.Headroom.LastLatencyMS)
	}
	if dash.Headroom.LastCheckedAt == nil || !dash.Headroom.LastCheckedAt.Equal(now) {
		t.Fatalf("dash LastCheckedAt = %#v", dash.Headroom.LastCheckedAt)
	}
	if dash.Headroom.LastOutcome != string(headroom.OutcomeApplied) {
		t.Fatalf("dash LastOutcome = %q", dash.Headroom.LastOutcome)
	}
}

func TestHeadroomCheckErrorCasesAndCooldown(t *testing.T) {
	now := time.Date(2026, time.August, 21, 10, 0, 0, 0, time.UTC)
	currentTime := now

	// 1. Disabled case -> 409
	storeDisabled, _ := config.NewStore([]byte("headroom_enabled: false\n"))
	handlerDisabled := NewHandler(Options{
		Now:            func() time.Time { return currentTime },
		ConfigSnapshot: func() *config.Store { return storeDisabled },
	})
	respDisabled := handlerDisabled.Handle(context.Background(), Request{Method: http.MethodPost, Path: ManagementBasePath + HeadroomCheckRoute})
	if respDisabled.StatusCode != http.StatusConflict {
		t.Fatalf("disabled status = %d, want 409", respDisabled.StatusCode)
	}

	// 2. Cold invalid config -> 409
	storeInvalid, _ := config.NewStore([]byte("headroom_enabled: true\nheadroom_url: http://invalid.example\n"))
	handlerInvalid := NewHandler(Options{
		Now:            func() time.Time { return currentTime },
		ConfigSnapshot: func() *config.Store { return storeInvalid },
	})
	respInvalid := handlerInvalid.Handle(context.Background(), Request{Method: http.MethodPost, Path: ManagementBasePath + HeadroomCheckRoute})
	if respInvalid.StatusCode != http.StatusConflict {
		t.Fatalf("invalid status = %d, want 409", respInvalid.StatusCode)
	}

	// 3. Timeout / connection unreachable case -> 200 with reachable:false, status:degraded
	storeValid, _ := config.NewStore([]byte("headroom_enabled: true\nheadroom_url: http://127.0.0.1:8787\n"))
	handlerUnreachable := NewHandler(Options{
		Now:            func() time.Time { return currentTime },
		ConfigSnapshot: func() *config.Store { return storeValid },
		HeadroomCheck: func(ctx context.Context, u string, d time.Duration) headroom.CheckResult {
			return headroom.CheckResult{
				Reachable: false,
				Outcome:   headroom.OutcomeTimeout,
				Latency:   100 * time.Millisecond,
			}
		},
	})
	respUnreachable := handlerUnreachable.Handle(context.Background(), Request{Method: http.MethodPost, Path: ManagementBasePath + HeadroomCheckRoute})
	if respUnreachable.StatusCode != http.StatusOK {
		t.Fatalf("unreachable status = %d, want 200", respUnreachable.StatusCode)
	}
	var unreachCheck HeadroomCheckDTO
	if err := json.Unmarshal(respUnreachable.Body, &unreachCheck); err != nil {
		t.Fatal(err)
	}
	if unreachCheck.Reachable || unreachCheck.Status != HeadroomStatusDegraded || unreachCheck.Outcome != string(headroom.OutcomeTimeout) || unreachCheck.LatencyMS != 100 {
		t.Fatalf("unreachable check = %#v", unreachCheck)
	}

	// 4. Cooldown 2 seconds: immediate second request returns 429
	respCooldown := handlerUnreachable.Handle(context.Background(), Request{Method: http.MethodPost, Path: ManagementBasePath + HeadroomCheckRoute})
	if respCooldown.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("cooldown status = %d, want 429", respCooldown.StatusCode)
	}

	// After 1999ms -> still 429
	currentTime = currentTime.Add(1999 * time.Millisecond)
	respStillCooldown := handlerUnreachable.Handle(context.Background(), Request{Method: http.MethodPost, Path: ManagementBasePath + HeadroomCheckRoute})
	if respStillCooldown.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("1999ms cooldown status = %d, want 429", respStillCooldown.StatusCode)
	}

	// Exactly 2s later -> 200 OK
	currentTime = now.Add(2000 * time.Millisecond)
	respAfterCooldown := handlerUnreachable.Handle(context.Background(), Request{Method: http.MethodPost, Path: ManagementBasePath + HeadroomCheckRoute})
	if respAfterCooldown.StatusCode != http.StatusOK {
		t.Fatalf("2000ms status = %d, want 200", respAfterCooldown.StatusCode)
	}
}

func TestHeadroomCheckInFlightExclusivity(t *testing.T) {
	now := time.Date(2026, time.August, 21, 10, 0, 0, 0, time.UTC)
	storeValid, _ := config.NewStore([]byte("headroom_enabled: true\nheadroom_url: http://127.0.0.1:8787\n"))

	inFlightBlocker := make(chan struct{})
	started := make(chan struct{})

	handler := NewHandler(Options{
		Now:            func() time.Time { return now },
		ConfigSnapshot: func() *config.Store { return storeValid },
		HeadroomCheck: func(ctx context.Context, u string, d time.Duration) headroom.CheckResult {
			close(started)
			<-inFlightBlocker
			return headroom.CheckResult{Reachable: true, Outcome: headroom.OutcomeApplied, Latency: 10 * time.Millisecond}
		},
	})

	doneFirst := make(chan Response)
	go func() {
		doneFirst <- handler.Handle(context.Background(), Request{Method: http.MethodPost, Path: ManagementBasePath + HeadroomCheckRoute})
	}()

	<-started
	// Second concurrent check must get 429 (in-flight)
	respSecond := handler.Handle(context.Background(), Request{Method: http.MethodPost, Path: ManagementBasePath + HeadroomCheckRoute})
	if respSecond.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("in-flight second call status = %d, want 429", respSecond.StatusCode)
	}

	close(inFlightBlocker)
	respFirst := <-doneFirst
	if respFirst.StatusCode != http.StatusOK {
		t.Fatalf("first call status = %d, want 200", respFirst.StatusCode)
	}
}
