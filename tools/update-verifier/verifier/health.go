package verifier

func EvaluateRuntime(phase Phase, approval Approval, observation RuntimeObservation) Result {
	plugin := observation.Plugin
	if !plugin.Found {
		return phaseFailure(phase, CodePluginMissing)
	}
	if !plugin.Registered {
		return phaseFailure(phase, CodePluginNotRegistered)
	}
	if !plugin.EffectiveEnabled {
		return phaseFailure(phase, CodePluginNotEffective)
	}
	if plugin.Version != "" && plugin.Version != approval.Plugin.Version {
		return phaseFailure(phase, CodePluginVersionMismatch)
	}
	if observation.Before.ConfigGeneration != observation.After.ConfigGeneration ||
		observation.Before.ConfigDigest != observation.After.ConfigDigest {
		return blocked(CodeConfigRace)
	}
	if observation.ConfigDigest == "" || observation.ConfigDigest != observation.After.ConfigDigest {
		return blocked(CodeConfigDigestMismatch)
	}
	for _, status := range []Status{observation.Before, observation.After} {
		if status.BuildVersion != approval.Plugin.Version {
			return phaseFailure(phase, CodePluginVersionMismatch)
		}
		if status.ABIVersion != approval.Plugin.ABI {
			return phaseFailure(phase, CodeABIMismatch)
		}
		if status.RPCSchema != approval.Plugin.RPC {
			return phaseFailure(phase, CodeRPCMismatch)
		}
		if status.FixtureRevision != FixtureRevision {
			return phaseFailure(phase, CodeFixtureMismatch)
		}
		if !status.Live || !healthyTruth(status) {
			return phaseFailure(phase, CodeRuntimeUnhealthy)
		}
		if status.Config != "valid" {
			return phaseFailure(phase, CodeConfigInvalid)
		}
	}
	if observation.SelfTest.FixtureRevision != FixtureRevision {
		return phaseFailure(phase, CodeFixtureMismatch)
	}
	if observation.SelfTest.Result != "passed" {
		return phaseFailure(phase, CodeSelfTestFailed)
	}
	return compatible()
}

func healthyTruth(status Status) bool {
	switch status.Pipeline {
	case "all_bypassed":
		return status.Dependency == "disabled" && !status.HeadroomDesired && !status.HeadroomEffective
	case "active":
		switch status.Dependency {
		case "disabled":
			return !status.HeadroomDesired && !status.HeadroomEffective
		case "ready":
			return status.HeadroomDesired && status.HeadroomEffective
		case "degraded":
			return status.HeadroomDesired && !status.HeadroomEffective
		}
	}
	return false
}
