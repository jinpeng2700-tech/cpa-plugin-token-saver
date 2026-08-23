package compat_test

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/tools/compat-probe/probe"
)

func TestRealCandidateDispatch(t *testing.T) {
	candidate := os.Getenv("CLIPROXYAPI_CANDIDATE")
	if candidate == "" {
		t.Skip("CLIPROXYAPI_CANDIDATE is not set")
	}
	plugin := os.Getenv("TOKEN_SAVER_PLUGIN")
	if plugin == "" {
		t.Fatal("TOKEN_SAVER_PLUGIN must be set when CLIPROXYAPI_CANDIDATE is supplied")
	}

	report := probe.Run(t.Context(), probe.Options{
		CandidatePath: candidate,
		PluginPath:    plugin,
		Timeout:       45 * time.Second,
	})
	if !report.Compatible {
		t.Fatalf("real candidate compatibility failed with stable code %q", report.Code)
	}
	wantScenarios := []string{"all-off", "rtk", "headroom-rewrite", "headroom-timeout", "caveman", "ponytail", "fixed-order"}
	if report.PluginID != "token-saver" || report.PluginVersion != "1.2.2" || report.MarkerCount != 1 ||
		report.ConfigGeneration == 0 || report.ConfigDigest == "" || !slices.Equal(report.Scenarios, wantScenarios) {
		t.Fatalf("real dispatch evidence is incomplete: %#v", report)
	}
}

func TestRealCandidateCoreOnlyDispatch(t *testing.T) {
	candidate := os.Getenv("CLIPROXYAPI_CANDIDATE")
	if candidate == "" {
		t.Skip("CLIPROXYAPI_CANDIDATE is not set")
	}

	report := probe.Run(t.Context(), probe.Options{
		Mode:          probe.ModeCoreOnly,
		CandidatePath: candidate,
		Timeout:       45 * time.Second,
	})
	if !report.Compatible || report.PluginID != "" || report.MarkerCount != 0 {
		t.Fatalf("real core-only dispatch evidence is incomplete: %#v", report)
	}
}
