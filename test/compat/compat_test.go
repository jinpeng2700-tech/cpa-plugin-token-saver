package compat_test

import (
	"os"
	"testing"
	"time"

	"github.com/router-for-me/cpa-plugin-token-saver/tools/compat-probe/probe"
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
	if report.PluginID != "token-saver" || report.MarkerCount != 1 || report.ConfigGeneration == 0 || report.ConfigDigest == "" {
		t.Fatalf("real dispatch evidence is incomplete: %#v", report)
	}
}
