package headroom

import (
	"context"
	"time"
)

// CheckResult captures the functional probe result for an isolated endpoint check.
type CheckResult struct {
	Reachable bool
	Outcome   Outcome
	Latency   time.Duration
}

// Check runs a fresh, isolated probe without modifying production circuit or metrics state.
func Check(ctx context.Context, baseURL string, timeout time.Duration) CheckResult {
	client, err := NewClient(baseURL, timeout)
	if err != nil {
		return CheckResult{
			Reachable: false,
			Outcome:   classifyRequestError(err),
			Latency:   0,
		}
	}
	defer client.Close()

	start := time.Now()
	outcome := client.Probe(ctx)
	latency := time.Since(start)
	reachable := outcome == OutcomeApplied || outcome == OutcomeNoChange
	return CheckResult{
		Reachable: reachable,
		Outcome:   outcome,
		Latency:   latency,
	}
}
