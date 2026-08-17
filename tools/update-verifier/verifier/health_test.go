package verifier

import "testing"

func healthyObservation(approval Approval) RuntimeObservation {
	status := Status{
		BuildVersion:     approval.Plugin.Version,
		ABIVersion:       1,
		RPCSchema:        3,
		FixtureRevision:  "v1",
		Live:             true,
		Config:           "valid",
		ConfigGeneration: 7,
		ConfigDigest:     "digest-7",
		Pipeline:         "all_bypassed",
		Dependency:       "disabled",
	}
	return RuntimeObservation{
		Plugin:       PluginState{Found: true, Registered: true, EffectiveEnabled: true, Version: approval.Plugin.Version},
		Before:       status,
		After:        status,
		ConfigDigest: "digest-7",
		SelfTest:     SelfTest{FixtureRevision: "v1", Result: "passed"},
	}
}

func TestEvaluateRuntimeAcceptsAllBypassedAndHeadroomDegraded(t *testing.T) {
	approval := validApproval()
	allOff := healthyObservation(approval)
	if result := EvaluateRuntime(PhasePostInstall, approval, allOff); !result.Compatible || result.Code != CodeOK {
		t.Fatalf("all-off result = %#v", result)
	}

	degraded := healthyObservation(approval)
	degraded.Before.Pipeline = "active"
	degraded.After.Pipeline = "active"
	degraded.Before.Dependency = "degraded"
	degraded.After.Dependency = "degraded"
	degraded.Before.HeadroomDesired = true
	degraded.After.HeadroomDesired = true
	if result := EvaluateRuntime(PhasePostInstall, approval, degraded); !result.Compatible || result.Code != CodeOK {
		t.Fatalf("Headroom-degraded result = %#v", result)
	}
}

func TestEvaluateRuntimeClassifiesPreExistingDisabledAsBlocked(t *testing.T) {
	approval := validApproval()
	observation := healthyObservation(approval)
	observation.Plugin.EffectiveEnabled = false

	before := EvaluateRuntime(PhasePreflight, approval, observation)
	if before.Compatible || before.Classification != ClassificationBlocked || before.Code != CodePluginNotEffective {
		t.Fatalf("preflight result = %#v", before)
	}
	after := EvaluateRuntime(PhasePostInstall, approval, observation)
	if after.Compatible || after.Classification != ClassificationCandidateFailure || after.Code != CodePluginNotEffective {
		t.Fatalf("post-install result = %#v", after)
	}
}

func TestEvaluateRuntimeRejectsIdentityHealthAndConfigRaces(t *testing.T) {
	approval := validApproval()
	for _, tt := range []struct {
		name   string
		mutate func(*RuntimeObservation)
		code   string
		class  string
	}{
		{name: "missing", mutate: func(o *RuntimeObservation) { o.Plugin.Found = false }, code: CodePluginMissing, class: ClassificationCandidateFailure},
		{name: "unregistered", mutate: func(o *RuntimeObservation) { o.Plugin.Registered = false }, code: CodePluginNotRegistered, class: ClassificationCandidateFailure},
		{name: "wrong build", mutate: func(o *RuntimeObservation) { o.Before.BuildVersion, o.After.BuildVersion = "dev", "dev" }, code: CodePluginVersionMismatch, class: ClassificationCandidateFailure},
		{name: "wrong ABI", mutate: func(o *RuntimeObservation) { o.Before.ABIVersion, o.After.ABIVersion = 2, 2 }, code: CodeABIMismatch, class: ClassificationCandidateFailure},
		{name: "wrong RPC", mutate: func(o *RuntimeObservation) { o.Before.RPCSchema, o.After.RPCSchema = 4, 4 }, code: CodeRPCMismatch, class: ClassificationCandidateFailure},
		{name: "wrong fixture", mutate: func(o *RuntimeObservation) { o.Before.FixtureRevision, o.After.FixtureRevision = "v2", "v2" }, code: CodeFixtureMismatch, class: ClassificationCandidateFailure},
		{name: "config invalid", mutate: func(o *RuntimeObservation) { o.Before.Config, o.After.Config = "config_error", "config_error" }, code: CodeConfigInvalid, class: ClassificationCandidateFailure},
		{name: "generation race", mutate: func(o *RuntimeObservation) { o.After.ConfigGeneration++ }, code: CodeConfigRace, class: ClassificationBlocked},
		{name: "digest race", mutate: func(o *RuntimeObservation) { o.After.ConfigDigest = "digest-8" }, code: CodeConfigRace, class: ClassificationBlocked},
		{name: "digest mismatch", mutate: func(o *RuntimeObservation) { o.ConfigDigest = "computed-other" }, code: CodeConfigDigestMismatch, class: ClassificationBlocked},
		{name: "self-test", mutate: func(o *RuntimeObservation) { o.SelfTest.Result = "failed" }, code: CodeSelfTestFailed, class: ClassificationCandidateFailure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			observation := healthyObservation(approval)
			tt.mutate(&observation)
			result := EvaluateRuntime(PhasePostInstall, approval, observation)
			if result.Compatible || result.Code != tt.code || result.Classification != tt.class {
				t.Fatalf("EvaluateRuntime() = %#v; want code=%q class=%q", result, tt.code, tt.class)
			}
		})
	}
}
