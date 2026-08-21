package metrics

import (
	"testing"
	"time"
)

func TestRegistryUsesFixedStageProjectionAndResetsOnRestart(t *testing.T) {
	started := time.Unix(100, 0)
	registry := New(started)
	registry.Record(StageRTK, OutcomeExecuted, 100, 60, 2*time.Millisecond)
	registry.Record(StageHeadroom, OutcomeTimeout, 60, 60, 5*time.Millisecond)
	registry.Record(Stage("model-secret"), Outcome("request-secret"), 1, 1, time.Millisecond)

	snapshot := registry.Snapshot()
	if !snapshot.StartedAt.Equal(started) {
		t.Fatalf("started_at = %s", snapshot.StartedAt)
	}
	if snapshot.Stages.RTK.Executed != 1 || snapshot.Stages.RTK.InputBytes != 100 || snapshot.Stages.RTK.OutputBytes != 60 {
		t.Fatalf("RTK snapshot = %#v", snapshot.Stages.RTK)
	}
	if snapshot.Stages.Headroom.FailOpen != 1 || snapshot.Stages.Headroom.Timeout != 1 {
		t.Fatalf("Headroom snapshot = %#v", snapshot.Stages.Headroom)
	}
	if snapshot.Stages.Pipeline.FailOpen != 1 {
		t.Fatalf("unknown labels were not bounded: %#v", snapshot.Stages.Pipeline)
	}

	restarted := New(started.Add(time.Hour)).Snapshot()
	if restarted.Stages.RTK.Executed != 0 || restarted.Stages.Headroom.Timeout != 0 {
		t.Fatalf("restart retained counters: %#v", restarted.Stages)
	}
}

func TestRegistryTracksCurrentAndPreviousGenerationInflight(t *testing.T) {
	registry := New(time.Unix(200, 0))
	registry.PublishGeneration(1)
	endOld := registry.Begin(1)
	registry.PublishGeneration(2)
	endCurrent := registry.Begin(2)

	snapshot := registry.Snapshot()
	if snapshot.Current.Generation != 2 || snapshot.Current.InFlight != 1 {
		t.Fatalf("current = %#v", snapshot.Current)
	}
	if snapshot.Previous.Generation != 1 || snapshot.Previous.InFlight != 1 {
		t.Fatalf("previous = %#v", snapshot.Previous)
	}
	endOld()
	if got := registry.Snapshot().Previous.InFlight; got != 0 {
		t.Fatalf("previous in-flight after completion = %d", got)
	}
	endCurrent()
}

func TestRegistryRecordsAllBypassedUnderOneProjection(t *testing.T) {
	registry := New(time.Unix(300, 0))
	registry.RecordAllBypassed(42)
	snapshot := registry.Snapshot().Stages
	for name, stage := range map[string]StageSnapshot{
		"pipeline": snapshot.Pipeline,
		"rtk":      snapshot.RTK,
		"headroom": snapshot.Headroom,
		"caveman":  snapshot.Caveman,
		"ponytail": snapshot.Ponytail,
	} {
		if stage.Bypassed != 1 || stage.InputBytes != 42 || stage.OutputBytes != 42 {
			t.Fatalf("%s safe-off snapshot = %#v", name, stage)
		}
	}
}

func TestStageSnapshotSavedBytesNeverUnderflows(t *testing.T) {
	tests := []struct {
		input, output, want uint64
	}{
		{100, 40, 60},
		{40, 40, 0},
		{40, 100, 0},
	}
	for _, test := range tests {
		got := (StageSnapshot{InputBytes: test.input, OutputBytes: test.output}).SavedBytes()
		if got != test.want {
			t.Fatalf("SavedBytes(%d,%d) = %d, want %d", test.input, test.output, got, test.want)
		}
	}
}
