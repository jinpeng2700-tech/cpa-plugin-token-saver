package abi

import (
	"reflect"
	"testing"
)

func TestPluginVersionIsPinnedToRelease(t *testing.T) {
	if PluginVersion != "1.2.3" {
		t.Fatalf("PluginVersion = %q, want 1.2.3", PluginVersion)
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

func TestPluginRegistrationUsesUserRepositoryAndGenericUIFields(t *testing.T) {
	registration := pluginRegistration()
	if got := registration.Metadata.GitHubRepository; got != "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver" {
		t.Fatalf("repository = %q, want user-owned repository", got)
	}

	gotFields := registration.Metadata.ConfigFields
	wantNames := []string{
		"rtk_enabled",
		"headroom_enabled",
		"headroom_url",
		"headroom_timeout_ms",
		"caveman_enabled",
		"caveman_level",
		"ponytail_enabled",
		"ponytail_level",
		"model_allowlist",
	}
	if len(gotFields) != len(wantNames) {
		t.Fatalf("ConfigFields count = %d, want %d", len(gotFields), len(wantNames))
	}
	for index, wantName := range wantNames {
		if got := gotFields[index].Name; got != wantName {
			t.Fatalf("ConfigFields[%d] = %q, want %q", index, got, wantName)
		}
	}
	if !reflect.DeepEqual(gotFields[5].EnumValues, []string{"lite", "full", "ultra", "wenyan-lite", "wenyan", "wenyan-ultra"}) {
		t.Fatalf("caveman_level enum = %#v", gotFields[5].EnumValues)
	}
	if !reflect.DeepEqual(gotFields[7].EnumValues, []string{"lite", "full", "ultra"}) {
		t.Fatalf("ponytail_level enum = %#v", gotFields[7].EnumValues)
	}
}
