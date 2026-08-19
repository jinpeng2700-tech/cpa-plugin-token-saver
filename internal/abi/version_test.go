package abi

import "testing"

func TestPluginVersionIsPinnedToRelease(t *testing.T) {
	if PluginVersion != "1.0.1" {
		t.Fatalf("PluginVersion = %q, want 1.0.1", PluginVersion)
	}
}

func TestPluginRegistrationUsesInjectedBuildVersion(t *testing.T) {
	original := PluginVersion
	PluginVersion = "1.2.3"
	t.Cleanup(func() { PluginVersion = original })

	if got := pluginRegistration().Metadata.Version; got != "1.2.3" {
		t.Fatalf("registration version = %q, want injected build version", got)
	}
}
