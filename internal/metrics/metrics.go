// Package metrics records only fixed, low-cardinality token-saver aggregates.
package metrics

import (
	"sync"
	"time"
)

// Stage is one of the fixed pipeline stages. Arbitrary values are folded into
// the pipeline fail-open counter and are never retained as labels.
type Stage string

const (
	StagePipeline Stage = "pipeline"
	StageRTK      Stage = "rtk"
	StageHeadroom Stage = "headroom"
	StageCaveman  Stage = "caveman"
	StagePonytail Stage = "ponytail"
)

// Outcome is one fixed aggregate result.
type Outcome string

const (
	OutcomeExecuted  Outcome = "executed"
	OutcomeBypassed  Outcome = "bypassed"
	OutcomeFailOpen  Outcome = "fail_open"
	OutcomeTimeout   Outcome = "timeout"
	OutcomeSaturated Outcome = "saturated"
)

// StageSnapshot contains counters only; it cannot retain request content,
// models, URLs, IDs, headers, credentials, or raw errors.
type StageSnapshot struct {
	Executed     uint64 `json:"executed"`
	Bypassed     uint64 `json:"bypassed"`
	FailOpen     uint64 `json:"fail_open"`
	Timeout      uint64 `json:"timeout"`
	Saturated    uint64 `json:"saturated"`
	InputBytes   uint64 `json:"input_bytes"`
	OutputBytes  uint64 `json:"output_bytes"`
	DurationNano uint64 `json:"duration_ns"`
}

// StageProjection fixes the complete metric label set at compile time.
type StageProjection struct {
	Pipeline StageSnapshot `json:"pipeline"`
	RTK      StageSnapshot `json:"rtk"`
	Headroom StageSnapshot `json:"headroom"`
	Caveman  StageSnapshot `json:"caveman"`
	Ponytail StageSnapshot `json:"ponytail"`
}

// GenerationSnapshot exposes only current/previous generation counts.
type GenerationSnapshot struct {
	Generation uint64 `json:"generation"`
	InFlight   uint64 `json:"in_flight"`
}

// Snapshot is a safe copy suitable for the U6 status projection.
type Snapshot struct {
	StartedAt time.Time          `json:"started_at"`
	Stages    StageProjection    `json:"stages"`
	Current   GenerationSnapshot `json:"current"`
	Previous  GenerationSnapshot `json:"previous"`
}

// Registry owns process-lifetime counters. It starts empty on every process.
type Registry struct {
	mu        sync.Mutex
	startedAt time.Time
	stages    StageProjection
	current   uint64
	previous  uint64
	inflight  map[uint64]uint64
}

// New creates a process-local empty registry.
func New(startedAt time.Time) *Registry {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return &Registry{startedAt: startedAt, inflight: make(map[uint64]uint64)}
}

// Record updates a bounded stage aggregate.
func (registry *Registry) Record(stage Stage, outcome Outcome, inputBytes, outputBytes int, duration time.Duration) {
	if registry == nil {
		return
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	counter, validStage := registry.stageLocked(stage)
	if !validStage || !validOutcome(outcome) {
		counter = &registry.stages.Pipeline
		outcome = OutcomeFailOpen
	}
	switch outcome {
	case OutcomeExecuted:
		counter.Executed++
	case OutcomeBypassed:
		counter.Bypassed++
	case OutcomeTimeout:
		counter.FailOpen++
		counter.Timeout++
	case OutcomeSaturated:
		counter.FailOpen++
		counter.Saturated++
	default:
		counter.FailOpen++
	}
	if inputBytes > 0 {
		counter.InputBytes += uint64(inputBytes)
	}
	if outputBytes > 0 {
		counter.OutputBytes += uint64(outputBytes)
	}
	if duration > 0 {
		counter.DurationNano += uint64(duration)
	}
}

// RecordAllBypassed updates the safe-off path under one lock.
func (registry *Registry) RecordAllBypassed(size int) {
	if registry == nil {
		return
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for _, counter := range []*StageSnapshot{
		&registry.stages.RTK,
		&registry.stages.Headroom,
		&registry.stages.Caveman,
		&registry.stages.Ponytail,
		&registry.stages.Pipeline,
	} {
		counter.Bypassed++
		if size > 0 {
			counter.InputBytes += uint64(size)
			counter.OutputBytes += uint64(size)
		}
	}
}

// PublishGeneration makes one successfully configured generation current.
func (registry *Registry) PublishGeneration(generation uint64) {
	if registry == nil {
		return
	}
	registry.mu.Lock()
	if registry.current != generation {
		stale := registry.previous
		registry.previous = registry.current
		registry.current = generation
		if stale != registry.current && stale != registry.previous && registry.inflight[stale] == 0 {
			delete(registry.inflight, stale)
		}
	}
	registry.mu.Unlock()
}

// Begin increments one generation and returns an idempotent completion hook.
func (registry *Registry) Begin(generation uint64) func() {
	if registry == nil {
		return func() {}
	}
	registry.mu.Lock()
	registry.inflight[generation]++
	registry.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			registry.mu.Lock()
			if registry.inflight[generation] > 0 {
				registry.inflight[generation]--
			}
			if registry.inflight[generation] == 0 && generation != registry.current && generation != registry.previous {
				delete(registry.inflight, generation)
			}
			registry.mu.Unlock()
		})
	}
}

// Snapshot returns a value copy with no internal maps or request data.
func (registry *Registry) Snapshot() Snapshot {
	if registry == nil {
		return Snapshot{}
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return Snapshot{
		StartedAt: registry.startedAt,
		Stages:    registry.stages,
		Current:   GenerationSnapshot{Generation: registry.current, InFlight: registry.inflight[registry.current]},
		Previous:  GenerationSnapshot{Generation: registry.previous, InFlight: registry.inflight[registry.previous]},
	}
}

func (registry *Registry) stageLocked(stage Stage) (*StageSnapshot, bool) {
	switch stage {
	case StagePipeline:
		return &registry.stages.Pipeline, true
	case StageRTK:
		return &registry.stages.RTK, true
	case StageHeadroom:
		return &registry.stages.Headroom, true
	case StageCaveman:
		return &registry.stages.Caveman, true
	case StagePonytail:
		return &registry.stages.Ponytail, true
	default:
		return nil, false
	}
}

func validOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomeExecuted, OutcomeBypassed, OutcomeFailOpen, OutcomeTimeout, OutcomeSaturated:
		return true
	default:
		return false
	}
}

// SavedBytes returns non-negative saved bytes without uint64 underflow.
func (snapshot StageSnapshot) SavedBytes() uint64 {
	if snapshot.OutputBytes >= snapshot.InputBytes {
		return 0
	}
	return snapshot.InputBytes - snapshot.OutputBytes
}
