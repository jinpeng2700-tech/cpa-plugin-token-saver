package abi

import "testing"

func TestPluginRegistrationUsesInjectedBuildVersion(t *testing.T) {
	original := PluginVersion
	PluginVersion = "1.2.3"
	t.Cleanup(func() { PluginVersion = original })

	if got := pluginRegistration().Metadata.Version; got != "1.2.3" {
		t.Fatalf("registration version = %q, want injected build version", got)
	}
}
