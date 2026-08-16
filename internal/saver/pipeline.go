// Package saver composes the four token-saving stages into one fail-open request normalizer.
package saver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/router-for-me/cpa-plugin-token-saver/internal/config"
	"github.com/router-for-me/cpa-plugin-token-saver/internal/headroom"
	"github.com/router-for-me/cpa-plugin-token-saver/internal/metrics"
	"github.com/router-for-me/cpa-plugin-token-saver/internal/prompt"
	"github.com/router-for-me/cpa-plugin-token-saver/internal/protocol"
	"github.com/router-for-me/cpa-plugin-token-saver/internal/rtk"
	"github.com/tidwall/gjson"
)

const maxProviderPayloadBytes = 10 << 20

// Request is the complete CLIProxyAPI RequestTransformRequest RPC payload.
type Request struct {
	FromFormat string
	ToFormat   string
	Model      string
	Stream     bool
	Body       []byte
}

// StageFunc is an injectable local stage used by deterministic tests.
type StageFunc func(context.Context, []byte, Request, config.Config) ([]byte, error)

// HeadroomRunner is bound to one immutable configuration generation.
type HeadroomRunner interface {
	Apply(context.Context, []byte, Request) ([]byte, headroom.Outcome)
}

type headroomStatusRunner interface {
	Probe(context.Context) headroom.Outcome
	CircuitState() headroom.CircuitState
}

// HeadroomStatus is a fixed dependency projection for management health.
type HeadroomStatus struct {
	Effective bool
	Circuit   headroom.CircuitState
}

// HeadroomFactory constructs one generation-scoped runner and idle-connection closer.
type HeadroomFactory func(config.Config) (HeadroomRunner, func(), error)

// StageValidator can add stage-specific invariants in tests. The production
// validator always runs first and cannot be disabled.
type StageValidator func(metrics.Stage, []byte, []byte, Request) bool

// Options contains only seams needed for deterministic unit tests.
type Options struct {
	Now             func() time.Time
	RTK             StageFunc
	Caveman         StageFunc
	Ponytail        StageFunc
	HeadroomFactory HeadroomFactory
	ValidateStage   StageValidator
	ValidateFinal   func([]byte, Request) bool
}

type generationState struct {
	generation uint64
	config     config.Config
	headroom   HeadroomRunner
	close      func()
	inflight   uint64
	retired    bool
	closed     bool
}

// Service owns immutable configuration generations, the bounded dedupe cache,
// and process-lifetime metrics.
type Service struct {
	mu      sync.Mutex
	closed  bool
	current *generationState
	next    uint64
	options Options
	dedupe  *dedupeCache
	metrics *metrics.Registry
}

// NewService creates a safe-off service. The first successful configuration is generation 1.
func NewService(options Options) *Service {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.RTK == nil {
		options.RTK = func(_ context.Context, body []byte, request Request, _ config.Config) ([]byte, error) {
			return rtk.Apply(body, request.pair()), nil
		}
	}
	if options.Caveman == nil {
		options.Caveman = func(_ context.Context, body []byte, request Request, cfg config.Config) ([]byte, error) {
			return prompt.Inject(body, request.pair(), prompt.Options{CavemanLevel: cfg.CavemanLevel}), nil
		}
	}
	if options.Ponytail == nil {
		options.Ponytail = func(_ context.Context, body []byte, request Request, cfg config.Config) ([]byte, error) {
			return prompt.Inject(body, request.pair(), prompt.Options{PonytailLevel: cfg.PonytailLevel}), nil
		}
	}
	if options.HeadroomFactory == nil {
		options.HeadroomFactory = defaultHeadroomFactory
	}
	registry := metrics.New(options.Now())
	registry.PublishGeneration(0)
	return &Service{
		options: options,
		dedupe:  newDedupe(options.Now),
		metrics: registry,
		current: &generationState{config: cloneConfig(config.Defaults())},
	}
}

// Reconfigure publishes a complete new generation only after its optional
// Headroom client has been constructed successfully.
func (service *Service) Reconfigure(cfg config.Config) error {
	if service == nil {
		return errors.New("token saver service is nil")
	}
	cfg = cloneConfig(cfg)
	var runner HeadroomRunner
	var closeRunner func()
	var errFactory error
	if cfg.HeadroomEnabled {
		runner, closeRunner, errFactory = service.options.HeadroomFactory(cfg)
		if errFactory != nil {
			return errFactory
		}
		if runner == nil {
			if closeRunner != nil {
				closeRunner()
			}
			return errors.New("headroom factory returned no runner")
		}
	}

	service.mu.Lock()
	if service.closed {
		service.mu.Unlock()
		if closeRunner != nil {
			closeRunner()
		}
		return errors.New("token saver service is closed")
	}
	service.next++
	next := &generationState{
		generation: service.next,
		config:     cfg,
		headroom:   runner,
		close:      closeRunner,
	}
	old := service.current
	service.current = next
	if old != nil {
		old.retired = true
	}
	closer := closableLocked(old)
	service.metrics.PublishGeneration(next.generation)
	service.mu.Unlock()
	if closer != nil {
		closer()
	}
	return nil
}

// Normalize applies one immutable snapshot and always returns a provider body.
func (service *Service) Normalize(ctx context.Context, request Request) (result []byte) {
	original := cloneBytes(request.Body)
	request.Body = cloneBytes(original)
	state, release := service.acquire()
	if state == nil {
		return original
	}
	defer release()
	defer func() {
		if recover() != nil {
			service.metrics.Record(metrics.StagePipeline, metrics.OutcomeFailOpen, len(original), len(original), 0)
			result = original
		}
	}()

	cfg := state.config
	if !cfg.RTKEnabled && !cfg.HeadroomEnabled && !cfg.CavemanEnabled && !cfg.PonytailEnabled {
		service.recordAllBypassed(len(original))
		return original
	}
	if !cfg.AllowsModel(request.Model) || len(original) == 0 || len(original) > maxProviderPayloadBytes || !validProviderShape(original, request) {
		service.metrics.Record(metrics.StagePipeline, metrics.OutcomeBypassed, len(original), len(original), 0)
		return original
	}

	working := original
	if cfg.HeadroomEnabled && len(original) <= dedupeMaxItemBytes {
		key := dedupeKey(state.generation, request)
		intermediate, hit, errDedupe := service.dedupe.Do(key, func() ([]byte, error) {
			stageBody := working
			stageBody = service.runLocalStage(ctx, metrics.StageRTK, cfg.RTKEnabled, stageBody, request, cfg, service.options.RTK)
			stageBody = service.runHeadroomStage(ctx, stageBody, request, state)
			return stageBody, nil
		})
		if errDedupe != nil {
			working = original
			service.metrics.Record(metrics.StagePipeline, metrics.OutcomeFailOpen, len(original), len(original), 0)
		} else {
			working = intermediate
			if hit {
				service.metrics.Record(metrics.StageRTK, metrics.OutcomeBypassed, len(original), len(original), 0)
				service.metrics.Record(metrics.StageHeadroom, metrics.OutcomeBypassed, len(working), len(working), 0)
			}
		}
	} else {
		working = service.runLocalStage(ctx, metrics.StageRTK, cfg.RTKEnabled, working, request, cfg, service.options.RTK)
		service.metrics.Record(metrics.StageHeadroom, metrics.OutcomeBypassed, len(working), len(working), 0)
	}
	working = service.runLocalStage(ctx, metrics.StageCaveman, cfg.CavemanEnabled, working, request, cfg, service.options.Caveman)
	working = service.runLocalStage(ctx, metrics.StagePonytail, cfg.PonytailEnabled, working, request, cfg, service.options.Ponytail)

	if !validProviderShape(working, request) || service.options.ValidateFinal != nil && !service.options.ValidateFinal(working, request) {
		service.metrics.Record(metrics.StagePipeline, metrics.OutcomeFailOpen, len(original), len(original), 0)
		return original
	}
	outcome := metrics.OutcomeExecuted
	if bytes.Equal(working, original) {
		outcome = metrics.OutcomeBypassed
	}
	service.metrics.Record(metrics.StagePipeline, outcome, len(original), len(working), 0)
	return working
}

// Metrics returns the process-local aggregate registry for U6's safe projection.
func (service *Service) Metrics() *metrics.Registry {
	if service == nil {
		return nil
	}
	return service.metrics
}

// Generation reports the current successfully published generation.
func (service *Service) Generation() uint64 {
	if service == nil {
		return 0
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.current == nil {
		return 0
	}
	return service.current.generation
}

// HeadroomStatus probes the current generation without exposing configuration
// or counting the management probe as a provider request in-flight.
func (service *Service) HeadroomStatus(ctx context.Context) HeadroomStatus {
	if service == nil {
		return HeadroomStatus{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	service.mu.Lock()
	if service.closed || service.current == nil {
		service.mu.Unlock()
		return HeadroomStatus{}
	}
	state := service.current
	desired := state.config.HeadroomEnabled
	if !desired {
		service.mu.Unlock()
		return HeadroomStatus{Circuit: headroom.CircuitClosed}
	}
	state.inflight++
	runner, observable := state.headroom.(headroomStatusRunner)
	service.mu.Unlock()
	defer service.releaseStatusState(state)
	if !observable || runner == nil {
		return HeadroomStatus{Circuit: headroom.CircuitClosed}
	}
	outcome := runner.Probe(ctx)
	circuit := runner.CircuitState()
	return HeadroomStatus{
		Effective: (outcome == headroom.OutcomeApplied || outcome == headroom.OutcomeNoChange) && circuit == headroom.CircuitClosed,
		Circuit:   circuit,
	}
}

func (service *Service) releaseStatusState(state *generationState) {
	service.mu.Lock()
	if state != nil && state.inflight > 0 {
		state.inflight--
	}
	closer := closableLocked(state)
	service.mu.Unlock()
	if closer != nil {
		closer()
	}
}

// Close retires the current client. In-flight generations close only after release.
func (service *Service) Close() {
	if service == nil {
		return
	}
	service.mu.Lock()
	service.closed = true
	if service.current != nil {
		service.current.retired = true
	}
	closer := closableLocked(service.current)
	service.mu.Unlock()
	if closer != nil {
		closer()
	}
}

func (service *Service) acquire() (*generationState, func()) {
	if service == nil {
		return nil, func() {}
	}
	service.mu.Lock()
	if service.closed || service.current == nil {
		service.mu.Unlock()
		return nil, func() {}
	}
	state := service.current
	state.inflight++
	endMetric := service.metrics.Begin(state.generation)
	service.mu.Unlock()
	var once sync.Once
	return state, func() {
		once.Do(func() {
			service.mu.Lock()
			if state.inflight > 0 {
				state.inflight--
			}
			closer := closableLocked(state)
			service.mu.Unlock()
			endMetric()
			if closer != nil {
				closer()
			}
		})
	}
}

func closableLocked(state *generationState) func() {
	if state == nil || !state.retired || state.inflight != 0 || state.closed || state.close == nil {
		return nil
	}
	state.closed = true
	return state.close
}

func (service *Service) runLocalStage(ctx context.Context, stage metrics.Stage, enabled bool, body []byte, request Request, cfg config.Config, apply StageFunc) (result []byte) {
	saved := cloneBytes(body)
	started := service.options.Now()
	if !enabled {
		service.metrics.Record(stage, metrics.OutcomeBypassed, len(saved), len(saved), service.options.Now().Sub(started))
		return saved
	}
	defer func() {
		if recover() != nil {
			service.metrics.Record(stage, metrics.OutcomeFailOpen, len(saved), len(saved), service.options.Now().Sub(started))
			result = saved
		}
	}()
	stageInput := cloneBytes(saved)
	stageRequest := request
	stageRequest.Body = cloneBytes(request.Body)
	output, errApply := apply(ctx, stageInput, stageRequest, cfg)
	if errApply != nil || len(output) == 0 || !service.validStage(stage, saved, output, request, cfg) {
		service.metrics.Record(stage, metrics.OutcomeFailOpen, len(saved), len(saved), service.options.Now().Sub(started))
		return saved
	}
	outcome := metrics.OutcomeExecuted
	if bytes.Equal(saved, output) {
		outcome = metrics.OutcomeBypassed
	}
	service.metrics.Record(stage, outcome, len(saved), len(output), service.options.Now().Sub(started))
	return cloneBytes(output)
}

func (service *Service) runHeadroomStage(ctx context.Context, body []byte, request Request, state *generationState) (result []byte) {
	saved := cloneBytes(body)
	started := service.options.Now()
	if !state.config.HeadroomEnabled || state.headroom == nil {
		service.metrics.Record(metrics.StageHeadroom, metrics.OutcomeBypassed, len(saved), len(saved), service.options.Now().Sub(started))
		return saved
	}
	defer func() {
		if recover() != nil {
			service.metrics.Record(metrics.StageHeadroom, metrics.OutcomeFailOpen, len(saved), len(saved), service.options.Now().Sub(started))
			result = saved
		}
	}()
	stageRequest := request
	stageRequest.Body = cloneBytes(request.Body)
	output, headroomOutcome := state.headroom.Apply(ctx, cloneBytes(saved), stageRequest)
	metricOutcome := metricForHeadroom(headroomOutcome, saved, output)
	if metricOutcome == metrics.OutcomeFailOpen || metricOutcome == metrics.OutcomeTimeout || metricOutcome == metrics.OutcomeSaturated || len(output) == 0 || !service.validStage(metrics.StageHeadroom, saved, output, request, state.config) {
		if metricOutcome == metrics.OutcomeExecuted || metricOutcome == metrics.OutcomeBypassed {
			metricOutcome = metrics.OutcomeFailOpen
		}
		service.metrics.Record(metrics.StageHeadroom, metricOutcome, len(saved), len(saved), service.options.Now().Sub(started))
		return saved
	}
	service.metrics.Record(metrics.StageHeadroom, metricOutcome, len(saved), len(output), service.options.Now().Sub(started))
	return cloneBytes(output)
}

func (service *Service) validStage(stage metrics.Stage, before, after []byte, request Request, cfg config.Config) bool {
	if !validProviderShape(after, request) {
		return false
	}
	switch stage {
	case metrics.StageRTK:
		if !validRTKTransition(before, after, request.pair()) {
			return false
		}
	case metrics.StageCaveman:
		expected := prompt.Inject(before, request.pair(), prompt.Options{CavemanLevel: cfg.CavemanLevel})
		if !bytes.Equal(after, expected) {
			return false
		}
	case metrics.StagePonytail:
		expected := prompt.Inject(before, request.pair(), prompt.Options{PonytailLevel: cfg.PonytailLevel})
		if !bytes.Equal(after, expected) {
			return false
		}
	}
	return service.options.ValidateStage == nil || service.options.ValidateStage(stage, before, after, request)
}

func (service *Service) recordAllBypassed(size int) {
	service.metrics.RecordAllBypassed(size)
}

func metricForHeadroom(outcome headroom.Outcome, input, output []byte) metrics.Outcome {
	switch outcome {
	case headroom.OutcomeApplied:
		if bytes.Equal(input, output) {
			return metrics.OutcomeBypassed
		}
		return metrics.OutcomeExecuted
	case headroom.OutcomeNoChange, headroom.OutcomeUnsupportedFormat, headroom.OutcomeUnsupportedStructure,
		headroom.OutcomeRequestTooLarge, headroom.OutcomeCircuitOpen:
		return metrics.OutcomeBypassed
	case headroom.OutcomeTimeout:
		return metrics.OutcomeTimeout
	case headroom.OutcomeSaturated:
		return metrics.OutcomeSaturated
	default:
		return metrics.OutcomeFailOpen
	}
}

type adapterRunner struct {
	adapter *headroom.Adapter
	client  *headroom.Client
}

func (runner adapterRunner) Apply(ctx context.Context, body []byte, request Request) ([]byte, headroom.Outcome) {
	return runner.adapter.Apply(ctx, body, request.pair(), request.Model)
}

func (runner adapterRunner) Probe(ctx context.Context) headroom.Outcome {
	return runner.client.Probe(ctx)
}

func (runner adapterRunner) CircuitState() headroom.CircuitState {
	return runner.client.CircuitState()
}

func defaultHeadroomFactory(cfg config.Config) (HeadroomRunner, func(), error) {
	client, errClient := headroom.NewClient(cfg.HeadroomURL, time.Duration(cfg.HeadroomTimeoutMS)*time.Millisecond)
	if errClient != nil {
		return nil, nil, errClient
	}
	return adapterRunner{adapter: headroom.NewAdapter(client), client: client}, client.CloseIdleConnections, nil
}

func (request Request) pair() protocol.Pair {
	return protocol.Pair{From: request.FromFormat, To: request.ToFormat}
}

func cloneConfig(cfg config.Config) config.Config {
	cfg.ModelAllowlist = append([]string(nil), cfg.ModelAllowlist...)
	if cfg.ModelAllowlist == nil {
		cfg.ModelAllowlist = []string{}
	}
	cfg.RawYAML = cloneBytes(cfg.RawYAML)
	return cfg
}

func validRTKTransition(before, after []byte, pair protocol.Pair) bool {
	if bytes.Equal(before, after) {
		return true
	}
	beforeView, beforeOK := protocol.View(before, pair)
	afterView, afterOK := protocol.View(after, pair)
	if !beforeOK || !afterOK {
		return false
	}
	beforeSlots := beforeView.Slots()
	afterSlots := afterView.Slots()
	if len(beforeSlots) != len(afterSlots) {
		return false
	}
	replacements := make(map[int]string, len(afterSlots))
	for index := range beforeSlots {
		if beforeSlots[index].ResultID != afterSlots[index].ResultID || beforeSlots[index].Kind != afterSlots[index].Kind || beforeSlots[index].Error != afterSlots[index].Error {
			return false
		}
		replacements[index] = afterSlots[index].Text
	}
	return bytes.Equal(beforeView.Rewrite(replacements), after)
}

func validProviderShape(body []byte, request Request) bool {
	if len(body) == 0 || len(body) > maxProviderPayloadBytes || !json.Valid(body) || !request.pair().Eligible() {
		return false
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return false
	}
	switch request.ToFormat {
	case "openai":
		return validRoleArray(root.Get("messages"), map[string]bool{
			"system": true, "developer": true, "user": true, "assistant": true, "tool": true, "function": true,
		})
	case "openai-response", "codex":
		items := root.Get("input")
		if !items.IsArray() || len(items.Array()) == 0 {
			return false
		}
		for _, item := range items.Array() {
			if !item.IsObject() {
				return false
			}
			role := item.Get("role")
			kind := item.Get("type")
			if role.Type != gjson.String && (kind.Type != gjson.String || kind.String() == "") {
				return false
			}
		}
		return true
	case "claude":
		return validRoleArray(root.Get("messages"), map[string]bool{"user": true, "assistant": true})
	case "gemini":
		contents := root.Get("contents")
		if !contents.IsArray() || len(contents.Array()) == 0 {
			return false
		}
		for _, content := range contents.Array() {
			if !content.IsObject() || !content.Get("parts").IsArray() {
				return false
			}
			role := content.Get("role").String()
			if role != "user" && role != "model" {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func validRoleArray(items gjson.Result, allowed map[string]bool) bool {
	if !items.IsArray() || len(items.Array()) == 0 {
		return false
	}
	for _, item := range items.Array() {
		if !item.IsObject() || !allowed[item.Get("role").String()] {
			return false
		}
	}
	return true
}
