package saver

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/config"
	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/headroom"
	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/prompt"
)

func TestDefaultHeadroomRunnerSupportsPassiveSnapshot(t *testing.T) {
	runner, closeRunner, err := defaultHeadroomFactory(config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer closeRunner()
	if _, ok := runner.(headroomSnapshotRunner); !ok {
		t.Fatalf("default Headroom runner %T does not expose passive snapshot", runner)
	}
}

func TestPipelineRunsEnabledStagesInFixedOrder(t *testing.T) {
	var order []string
	stage := func(name string) StageFunc {
		return func(_ context.Context, body []byte, _ Request, _ config.Config) ([]byte, error) {
			order = append(order, name)
			return body, nil
		}
	}
	runner := &headroomRunnerFunc{apply: func(_ context.Context, body []byte, _ Request) ([]byte, headroom.Outcome) {
		order = append(order, "headroom")
		return body, headroom.OutcomeNoChange
	}}
	service := NewService(Options{
		RTK:      stage("rtk"),
		Caveman:  stage("caveman"),
		Ponytail: stage("ponytail"),
		HeadroomFactory: func(config.Config) (HeadroomRunner, func(), error) {
			return runner, func() {}, nil
		},
	})
	defer service.Close()
	if err := service.Reconfigure(config.Config{
		RTKEnabled: true, HeadroomEnabled: true,
		CavemanEnabled: true, CavemanLevel: "lite",
		PonytailEnabled: true, PonytailLevel: "lite",
	}); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	got := service.Normalize(context.Background(), Request{
		FromFormat: "openai", ToFormat: "openai", Model: "model-a", Body: body,
	})
	if string(got) != string(body) {
		t.Fatalf("Normalize changed fixture: %s", got)
	}
	if want := []string{"rtk", "headroom", "caveman", "ponytail"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("stage order = %#v, want %#v", order, want)
	}
}

func TestPipelineRunsPromptStagesForAntigravityEnvelope(t *testing.T) {
	service := NewService(Options{})
	defer service.Close()
	if err := service.Reconfigure(config.Config{
		CavemanEnabled: true, CavemanLevel: "lite",
		PonytailEnabled: true, PonytailLevel: "ultra",
	}); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"project":"project-1","request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]},"model":"gemini-3.7-flash-high"}`)
	got := service.Normalize(context.Background(), Request{
		FromFormat: "openai-response", ToFormat: "antigravity", Model: "gemini-3.7-flash-high", Body: body,
	})
	if bytes.Equal(got, body) {
		t.Fatal("Antigravity prompt stages did not change the payload")
	}
	if strings.Count(string(got), "[CPA_TOKEN_SAVER_CAVEMAN_START]") != 1 || strings.Count(string(got), "[CPA_TOKEN_SAVER_PONYTAIL_START]") != 1 {
		t.Fatalf("Antigravity prompt markers missing or duplicated: %s", got)
	}
	if !bytes.Contains(got, []byte(`"project":"project-1"`)) {
		t.Fatal("Antigravity envelope fields changed")
	}
	snapshot := service.Metrics().Snapshot()
	if snapshot.Stages.Caveman.Executed != 1 || snapshot.Stages.Ponytail.Executed != 1 {
		t.Fatalf("Antigravity prompt metrics = caveman:%d ponytail:%d, want 1/1", snapshot.Stages.Caveman.Executed, snapshot.Stages.Ponytail.Executed)
	}
}

func TestPipelineAllOffIsByteIdenticalAndDoesNotConstructHeadroom(t *testing.T) {
	var factories atomic.Int32
	service := NewService(Options{HeadroomFactory: func(config.Config) (HeadroomRunner, func(), error) {
		factories.Add(1)
		return nil, nil, nil
	}})
	defer service.Close()
	if err := service.Reconfigure(config.Defaults()); err != nil {
		t.Fatal(err)
	}
	body := []byte("{\n  \"messages\" : [{\"role\":\"user\",\"content\":\"hello\"}], \"opaque\": 1.0\n}")
	got := service.Normalize(context.Background(), Request{FromFormat: "openai", ToFormat: "openai", Model: "m", Body: body})
	if !bytes.Equal(got, body) {
		t.Fatalf("all-off body changed: %s", got)
	}
	if factories.Load() != 0 {
		t.Fatalf("Headroom factories = %d", factories.Load())
	}
}

func TestPipelineIndependentSwitchesAndEligibility(t *testing.T) {
	var calls [4]atomic.Int32
	local := func(index int) StageFunc {
		return func(_ context.Context, body []byte, _ Request, _ config.Config) ([]byte, error) {
			calls[index].Add(1)
			return body, nil
		}
	}
	runner := &headroomRunnerFunc{apply: func(_ context.Context, body []byte, _ Request) ([]byte, headroom.Outcome) {
		calls[1].Add(1)
		return body, headroom.OutcomeNoChange
	}}
	service := NewService(Options{
		RTK: local(0), Caveman: local(2), Ponytail: local(3),
		HeadroomFactory: func(config.Config) (HeadroomRunner, func(), error) { return runner, func() {}, nil },
	})
	defer service.Close()
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	request := Request{FromFormat: "openai", ToFormat: "openai", Model: "allowed", Body: body}
	configs := []config.Config{
		{RTKEnabled: true},
		{HeadroomEnabled: true},
		{CavemanEnabled: true, CavemanLevel: "lite"},
		{PonytailEnabled: true, PonytailLevel: "lite"},
	}
	for index, cfg := range configs {
		for item := range calls {
			calls[item].Store(0)
		}
		if err := service.Reconfigure(cfg); err != nil {
			t.Fatal(err)
		}
		service.Normalize(context.Background(), request)
		for item := range calls {
			want := int32(0)
			if item == index {
				want = 1
			}
			if got := calls[item].Load(); got != want {
				t.Fatalf("config %d stage %d calls = %d, want %d", index, item, got, want)
			}
		}
	}

	for item := range calls {
		calls[item].Store(0)
	}
	if err := service.Reconfigure(config.Config{RTKEnabled: true, ModelAllowlist: []string{"other"}}); err != nil {
		t.Fatal(err)
	}
	service.Normalize(context.Background(), request)
	service.Normalize(context.Background(), Request{FromFormat: "plugin-format", ToFormat: "openai", Model: "other", Body: body})
	for index := range calls {
		if calls[index].Load() != 0 {
			t.Fatalf("ineligible requests called stage %d", index)
		}
	}
}

func TestPipelineStageFailuresContinueAndFinalValidationRestoresOriginal(t *testing.T) {
	var order []string
	service := NewService(Options{
		RTK: func(context.Context, []byte, Request, config.Config) ([]byte, error) {
			order = append(order, "rtk")
			panic("rtk panic")
		},
		Caveman: func(_ context.Context, body []byte, _ Request, _ config.Config) ([]byte, error) {
			order = append(order, "caveman")
			return bytes.Replace(body, []byte("hello"), []byte("changed"), 1), nil
		},
		Ponytail: func(_ context.Context, body []byte, _ Request, _ config.Config) ([]byte, error) {
			order = append(order, "ponytail")
			return body, nil
		},
		HeadroomFactory: func(config.Config) (HeadroomRunner, func(), error) {
			return &headroomRunnerFunc{apply: func(_ context.Context, body []byte, _ Request) ([]byte, headroom.Outcome) {
				order = append(order, "headroom")
				return body, headroom.OutcomeTimeout
			}}, func() {}, nil
		},
		ValidateFinal: func([]byte, Request) bool { return false },
	})
	defer service.Close()
	if err := service.Reconfigure(config.Config{
		RTKEnabled: true, HeadroomEnabled: true,
		CavemanEnabled: true, PonytailEnabled: true,
		CavemanLevel: "lite", PonytailLevel: "lite",
	}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	got := service.Normalize(context.Background(), Request{FromFormat: "openai", ToFormat: "openai", Model: "m", Body: body})
	if !bytes.Equal(got, body) {
		t.Fatalf("final validation did not restore original: %s", got)
	}
	if want := []string{"rtk", "headroom", "caveman", "ponytail"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("failure order = %#v, want %#v", order, want)
	}
	snapshot := service.Metrics().Snapshot()
	if snapshot.Stages.RTK.FailOpen != 1 || snapshot.Stages.Headroom.Timeout != 1 || snapshot.Stages.Pipeline.FailOpen != 1 {
		t.Fatalf("failure metrics = %#v", snapshot.Stages)
	}
}

func TestPipelineRequestKeepsOneSnapshotAcrossReconfigure(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var oldRTK, newCaveman atomic.Int32
	service := NewService(Options{
		RTK: func(_ context.Context, body []byte, _ Request, _ config.Config) ([]byte, error) {
			oldRTK.Add(1)
			once.Do(func() { close(started) })
			<-release
			return body, nil
		},
		Caveman: func(_ context.Context, body []byte, _ Request, _ config.Config) ([]byte, error) {
			newCaveman.Add(1)
			return body, nil
		},
	})
	defer service.Close()
	if err := service.Reconfigure(config.Config{RTKEnabled: true}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	request := Request{FromFormat: "openai", ToFormat: "openai", Model: "m", Body: body}
	done := make(chan struct{})
	go func() {
		service.Normalize(context.Background(), request)
		close(done)
	}()
	<-started
	if err := service.Reconfigure(config.Config{CavemanEnabled: true, CavemanLevel: "lite"}); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	if oldRTK.Load() != 1 || newCaveman.Load() != 0 {
		t.Fatalf("in-flight stages = rtk:%d caveman:%d", oldRTK.Load(), newCaveman.Load())
	}
	service.Normalize(context.Background(), request)
	if oldRTK.Load() != 1 || newCaveman.Load() != 1 {
		t.Fatalf("next request stages = rtk:%d caveman:%d", oldRTK.Load(), newCaveman.Load())
	}
}

func TestPipelineDoubleNormalizeCallsHeadroomOnceButRunsPromptsEachTime(t *testing.T) {
	var headroomCalls, promptCalls atomic.Int32
	service := NewService(Options{
		HeadroomFactory: func(config.Config) (HeadroomRunner, func(), error) {
			return &headroomRunnerFunc{apply: func(_ context.Context, body []byte, _ Request) ([]byte, headroom.Outcome) {
				headroomCalls.Add(1)
				return body, headroom.OutcomeNoChange
			}}, func() {}, nil
		},
		Caveman: func(_ context.Context, body []byte, request Request, cfg config.Config) ([]byte, error) {
			promptCalls.Add(1)
			return prompt.Inject(body, request.pair(), prompt.Options{CavemanLevel: cfg.CavemanLevel}), nil
		},
	})
	defer service.Close()
	if err := service.Reconfigure(config.Config{HeadroomEnabled: true, CavemanEnabled: true, CavemanLevel: "lite"}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"messages":[{"role":"user","content":"hello"}],"opaque":"` + strings.Repeat("x", (1<<20)-512) + `"}`)
	request := Request{FromFormat: "openai", ToFormat: "openai", Model: "m", Stream: true, Body: body}
	first := service.Normalize(context.Background(), request)
	second := service.Normalize(context.Background(), request)
	if headroomCalls.Load() != 1 || promptCalls.Load() != 2 {
		t.Fatalf("double normalize calls = headroom:%d prompt:%d", headroomCalls.Load(), promptCalls.Load())
	}
	if len(first) <= 1<<20 || len(second) <= 1<<20 {
		t.Fatalf("post-cache prompt output did not cross 1 MiB: %d / %d", len(first), len(second))
	}
	if got := service.Metrics().Snapshot().Stages.RTK.Bypassed; got != 2 {
		t.Fatalf("RTK bypass count = %d, want 2", got)
	}
}

func TestPipelinePromptStageCannotModifyUserContent(t *testing.T) {
	var ponytailInput []byte
	service := NewService(Options{
		Caveman: func(_ context.Context, body []byte, _ Request, _ config.Config) ([]byte, error) {
			return bytes.Replace(body, []byte("hello"), []byte("changed"), 1), nil
		},
		Ponytail: func(_ context.Context, body []byte, _ Request, _ config.Config) ([]byte, error) {
			ponytailInput = cloneBytes(body)
			return body, nil
		},
	})
	defer service.Close()
	if err := service.Reconfigure(config.Config{
		CavemanEnabled: true, CavemanLevel: "lite",
		PonytailEnabled: true, PonytailLevel: "lite",
	}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	got := service.Normalize(context.Background(), Request{FromFormat: "openai", ToFormat: "openai", Model: "m", Body: body})
	if !bytes.Equal(got, body) || !bytes.Equal(ponytailInput, body) {
		t.Fatalf("prompt mutation escaped stage transaction: output=%s next=%s", got, ponytailInput)
	}
}

func TestPipelineDoesNotCacheRTKWhenHeadroomIsDisabled(t *testing.T) {
	var calls atomic.Int32
	service := NewService(Options{RTK: func(_ context.Context, body []byte, _ Request, _ config.Config) ([]byte, error) {
		calls.Add(1)
		return body, nil
	}})
	defer service.Close()
	if err := service.Reconfigure(config.Config{RTKEnabled: true}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	request := Request{FromFormat: "openai", ToFormat: "openai", Model: "m", Body: body}
	service.Normalize(context.Background(), request)
	service.Normalize(context.Background(), request)
	if calls.Load() != 2 {
		t.Fatalf("RTK-only calls = %d, want 2", calls.Load())
	}
}

func TestPipelineHeadroomSaturationStillRunsLaterPrompts(t *testing.T) {
	var promptCalls atomic.Int32
	service := NewService(Options{
		HeadroomFactory: func(config.Config) (HeadroomRunner, func(), error) {
			return &headroomRunnerFunc{apply: func(_ context.Context, body []byte, _ Request) ([]byte, headroom.Outcome) {
				return body, headroom.OutcomeSaturated
			}}, func() {}, nil
		},
		Ponytail: func(_ context.Context, body []byte, _ Request, _ config.Config) ([]byte, error) {
			promptCalls.Add(1)
			return body, nil
		},
	})
	defer service.Close()
	if err := service.Reconfigure(config.Config{HeadroomEnabled: true, PonytailEnabled: true}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	service.Normalize(context.Background(), Request{FromFormat: "openai", ToFormat: "openai", Model: "m", Body: body})
	if promptCalls.Load() != 1 || service.Metrics().Snapshot().Stages.Headroom.Saturated != 1 {
		t.Fatalf("saturation flow = prompts:%d metrics:%#v", promptCalls.Load(), service.Metrics().Snapshot().Stages.Headroom)
	}
}

func TestPipelineConcurrentNormalizeCoalescesHeadroom(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	service := NewService(Options{HeadroomFactory: func(config.Config) (HeadroomRunner, func(), error) {
		return &headroomRunnerFunc{apply: func(_ context.Context, body []byte, _ Request) ([]byte, headroom.Outcome) {
			calls.Add(1)
			once.Do(func() { close(started) })
			<-release
			return body, headroom.OutcomeNoChange
		}}, func() {}, nil
	}})
	defer service.Close()
	if err := service.Reconfigure(config.Config{HeadroomEnabled: true}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	request := Request{FromFormat: "openai", ToFormat: "openai", Model: "m", Body: body}
	done := make(chan struct{}, 2)
	go func() { service.Normalize(context.Background(), request); done <- struct{}{} }()
	<-started
	go func() { service.Normalize(context.Background(), request); done <- struct{}{} }()
	close(release)
	<-done
	<-done
	if calls.Load() != 1 {
		t.Fatalf("concurrent Headroom calls = %d", calls.Load())
	}
}

func TestPipelineOriginalOverOneMiBRunsRTKWithoutHeadroom(t *testing.T) {
	var rtkCalls, headroomCalls atomic.Int32
	service := NewService(Options{
		RTK: func(_ context.Context, body []byte, _ Request, _ config.Config) ([]byte, error) {
			rtkCalls.Add(1)
			return body, nil
		},
		HeadroomFactory: func(config.Config) (HeadroomRunner, func(), error) {
			return &headroomRunnerFunc{apply: func(_ context.Context, body []byte, _ Request) ([]byte, headroom.Outcome) {
				headroomCalls.Add(1)
				return body, headroom.OutcomeNoChange
			}}, func() {}, nil
		},
	})
	defer service.Close()
	if err := service.Reconfigure(config.Config{RTKEnabled: true, HeadroomEnabled: true}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"messages":[{"role":"user","content":"hello"}],"opaque":"` + strings.Repeat("x", (1<<20)+1) + `"}`)
	service.Normalize(context.Background(), Request{FromFormat: "openai", ToFormat: "openai", Model: "m", Body: body})
	if rtkCalls.Load() != 1 || headroomCalls.Load() != 0 {
		t.Fatalf("large request calls = rtk:%d headroom:%d", rtkCalls.Load(), headroomCalls.Load())
	}
}

func TestPipelineRetiresHeadroomClientAfterOldGenerationCompletes(t *testing.T) {
	oldStarted := make(chan struct{})
	oldRelease := make(chan struct{})
	var factories, oldClosed, newClosed atomic.Int32
	service := NewService(Options{HeadroomFactory: func(config.Config) (HeadroomRunner, func(), error) {
		generation := factories.Add(1)
		if generation == 1 {
			return &headroomRunnerFunc{apply: func(_ context.Context, body []byte, _ Request) ([]byte, headroom.Outcome) {
				close(oldStarted)
				<-oldRelease
				return body, headroom.OutcomeNoChange
			}}, func() { oldClosed.Add(1) }, nil
		}
		return &headroomRunnerFunc{apply: func(_ context.Context, body []byte, _ Request) ([]byte, headroom.Outcome) {
			return body, headroom.OutcomeNoChange
		}}, func() { newClosed.Add(1) }, nil
	}})
	if err := service.Reconfigure(config.Config{HeadroomEnabled: true}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	request := Request{FromFormat: "openai", ToFormat: "openai", Model: "m", Body: body}
	done := make(chan struct{})
	go func() {
		service.Normalize(context.Background(), request)
		close(done)
	}()
	<-oldStarted
	if err := service.Reconfigure(config.Config{HeadroomEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if oldClosed.Load() != 0 {
		t.Fatal("old client closed while its request was in flight")
	}
	snapshot := service.Metrics().Snapshot()
	if snapshot.Current.Generation != 2 || snapshot.Previous.Generation != 1 || snapshot.Previous.InFlight != 1 {
		t.Fatalf("generation snapshot during switch = %#v/%#v", snapshot.Current, snapshot.Previous)
	}
	service.Normalize(context.Background(), request)
	close(oldRelease)
	<-done
	if oldClosed.Load() != 1 || service.Metrics().Snapshot().Previous.InFlight != 0 {
		t.Fatalf("old lifecycle = closed:%d previous:%#v", oldClosed.Load(), service.Metrics().Snapshot().Previous)
	}
	service.Close()
	if newClosed.Load() != 1 {
		t.Fatalf("new client close count = %d", newClosed.Load())
	}
}

func TestPipelineFailedReconfigureKeepsGenerationAndRunner(t *testing.T) {
	var factories, calls atomic.Int32
	service := NewService(Options{HeadroomFactory: func(config.Config) (HeadroomRunner, func(), error) {
		if factories.Add(1) == 2 {
			return nil, nil, context.DeadlineExceeded
		}
		return &headroomRunnerFunc{apply: func(_ context.Context, body []byte, _ Request) ([]byte, headroom.Outcome) {
			calls.Add(1)
			return body, headroom.OutcomeNoChange
		}}, func() {}, nil
	}})
	defer service.Close()
	if err := service.Reconfigure(config.Config{HeadroomEnabled: true}); err != nil {
		t.Fatal(err)
	}
	wantGeneration := service.Generation()
	if err := service.Reconfigure(config.Config{HeadroomEnabled: true, CavemanEnabled: true}); err == nil {
		t.Fatal("failed Headroom construction unexpectedly published")
	}
	if service.Generation() != wantGeneration {
		t.Fatalf("generation advanced from %d to %d", wantGeneration, service.Generation())
	}
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	service.Normalize(context.Background(), Request{FromFormat: "openai", ToFormat: "openai", Model: "m", Body: body})
	if calls.Load() != 1 {
		t.Fatalf("last-known-good runner calls = %d", calls.Load())
	}
}

func TestPipelineInvalidStageOutputRestoresStageInputAndContinues(t *testing.T) {
	var ponytailInput []byte
	service := NewService(Options{
		Caveman: func(context.Context, []byte, Request, config.Config) ([]byte, error) {
			return []byte(`{"messages":`), nil
		},
		Ponytail: func(_ context.Context, body []byte, _ Request, _ config.Config) ([]byte, error) {
			ponytailInput = cloneBytes(body)
			return body, nil
		},
	})
	defer service.Close()
	if err := service.Reconfigure(config.Config{CavemanEnabled: true, PonytailEnabled: true}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	got := service.Normalize(context.Background(), Request{FromFormat: "openai", ToFormat: "openai", Model: "m", Body: body})
	if !bytes.Equal(got, body) || !bytes.Equal(ponytailInput, body) {
		t.Fatalf("stage recovery = output:%s ponytail-input:%s", got, ponytailInput)
	}
}

func TestPipelinePanicCannotMutateSavedStageOrRequestBody(t *testing.T) {
	var nextInput []byte
	service := NewService(Options{
		RTK: func(_ context.Context, body []byte, request Request, _ config.Config) ([]byte, error) {
			body[0] = 'X'
			request.Body[0] = 'Y'
			panic("after mutation")
		},
		Ponytail: func(_ context.Context, body []byte, _ Request, _ config.Config) ([]byte, error) {
			nextInput = cloneBytes(body)
			return body, nil
		},
	})
	defer service.Close()
	if err := service.Reconfigure(config.Config{RTKEnabled: true, PonytailEnabled: true}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	got := service.Normalize(context.Background(), Request{FromFormat: "openai", ToFormat: "openai", Model: "m", Body: body})
	if !bytes.Equal(got, body) || !bytes.Equal(nextInput, body) {
		t.Fatalf("panic mutation escaped transaction: output=%s next=%s original=%s", got, nextInput, body)
	}
}

type headroomRunnerFunc struct {
	apply func(context.Context, []byte, Request) ([]byte, headroom.Outcome)
}

func (runner *headroomRunnerFunc) Apply(ctx context.Context, body []byte, request Request) ([]byte, headroom.Outcome) {
	return runner.apply(ctx, body, request)
}
