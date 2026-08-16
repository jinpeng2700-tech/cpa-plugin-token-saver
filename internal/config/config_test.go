package config

import (
	"bytes"
	"reflect"
	"sync"
	"testing"
)

func TestStoreDefaultsAreSafeOff(t *testing.T) {
	store, err := NewStore(nil)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	got := store.Snapshot()
	if got.RTKEnabled || got.HeadroomEnabled || got.CavemanEnabled || got.PonytailEnabled {
		t.Fatalf("default stages = %#v, want all disabled", got)
	}
	if got.HeadroomURL != "http://127.0.0.1:8787" {
		t.Fatalf("HeadroomURL = %q", got.HeadroomURL)
	}
	if got.HeadroomTimeoutMS != 1500 {
		t.Fatalf("HeadroomTimeoutMS = %d", got.HeadroomTimeoutMS)
	}
	if got.CavemanLevel != "full" || got.PonytailLevel != "full" {
		t.Fatalf("levels = %q/%q, want full/full", got.CavemanLevel, got.PonytailLevel)
	}
	if got.ModelAllowlist == nil || len(got.ModelAllowlist) != 0 {
		t.Fatalf("ModelAllowlist = %#v, want non-nil empty", got.ModelAllowlist)
	}
	if endpoint := got.HeadroomEndpoint(); endpoint != "http://127.0.0.1:8787/v1/compress" {
		t.Fatalf("HeadroomEndpoint() = %q", endpoint)
	}
	if !got.AllowsModel("any/model") {
		t.Fatal("empty model_allowlist must allow every exact model string")
	}
}

func TestStoreLoadsCompleteConfigAndPreservesUnknownFields(t *testing.T) {
	raw := []byte(`
enabled: true
priority: 9
store: official
rtk_enabled: true
headroom_enabled: true
headroom_url: http://[::1]:8787/
headroom_timeout_ms: 100
caveman_enabled: true
caveman_level: wenyan-ultra
ponytail_enabled: true
ponytail_level: ultra
model_allowlist:
  - claude-3-7-sonnet
  - GPT-5.Exact
future_plugin_field:
  nested: preserved
`)
	store, err := NewStore(raw)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	got := store.Snapshot()
	if !got.RTKEnabled || !got.HeadroomEnabled || !got.CavemanEnabled || !got.PonytailEnabled {
		t.Fatalf("loaded stages = %#v, want all enabled", got)
	}
	if got.HeadroomURL != "http://[::1]:8787" {
		t.Fatalf("HeadroomURL = %q, want normalized IPv6 base URL", got.HeadroomURL)
	}
	if got.HeadroomEndpoint() != "http://[::1]:8787/v1/compress" {
		t.Fatalf("HeadroomEndpoint() = %q", got.HeadroomEndpoint())
	}
	if got.HeadroomTimeoutMS != 100 || got.CavemanLevel != "wenyan-ultra" || got.PonytailLevel != "ultra" {
		t.Fatalf("loaded config = %#v", got)
	}
	wantModels := []string{"claude-3-7-sonnet", "GPT-5.Exact"}
	if !reflect.DeepEqual(got.ModelAllowlist, wantModels) {
		t.Fatalf("ModelAllowlist = %#v, want %#v", got.ModelAllowlist, wantModels)
	}
	if !got.AllowsModel("GPT-5.Exact") || got.AllowsModel("gpt-5.exact") || got.AllowsModel("GPT-5") {
		t.Fatal("model_allowlist matching must be exact and case-sensitive")
	}
	if !bytes.Contains(got.RawYAML, []byte("future_plugin_field:")) || !bytes.Contains(got.RawYAML, []byte("nested: preserved")) {
		t.Fatalf("RawYAML did not preserve unknown fields: %q", got.RawYAML)
	}
}

func TestNewStoreInvalidColdConfigFallsBackToSafeOff(t *testing.T) {
	store, err := NewStore([]byte("rtk_enabled: true\ncaveman_level: verbose\n"))
	if err == nil {
		t.Fatal("NewStore() error = nil, want invalid enum error")
	}
	got := store.Snapshot()
	if got.RTKEnabled || got.HeadroomEnabled || got.CavemanEnabled || got.PonytailEnabled {
		t.Fatalf("cold fallback stages = %#v, want all disabled", got)
	}
}

func TestReloadInvalidConfigKeepsLastKnownGoodSnapshot(t *testing.T) {
	store, err := NewStore([]byte("rtk_enabled: true\nmodel_allowlist: [model-a]\n"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	want := store.Snapshot()

	if errReload := store.Reload([]byte("rtk_enabled: false\nheadroom_timeout_ms: 1501\n")); errReload == nil {
		t.Fatal("Reload() error = nil, want timeout validation error")
	}
	got := store.Snapshot()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot after failed reload = %#v, want LKG %#v", got, want)
	}
}

func TestReloadPublishesIndependentSnapshotsConcurrently(t *testing.T) {
	store, err := NewStore(nil)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	const readers = 16
	const iterations = 200
	start := make(chan struct{})
	errCh := make(chan string, readers)
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				snapshot := store.Snapshot()
				if snapshot.CavemanLevel != "full" || snapshot.PonytailLevel != "full" {
					errCh <- "reader observed a partial snapshot"
					return
				}
				if len(snapshot.ModelAllowlist) > 0 {
					snapshot.ModelAllowlist[0] = "mutated-by-reader"
				}
				if len(snapshot.RawYAML) > 0 {
					snapshot.RawYAML[0] = 'X'
				}
			}
		}()
	}
	close(start)
	for i := 0; i < iterations; i++ {
		raw := []byte("model_allowlist: [model-a]\n")
		if i%2 == 1 {
			raw = []byte("rtk_enabled: true\nmodel_allowlist: [model-b]\n")
		}
		if errReload := store.Reload(raw); errReload != nil {
			t.Fatalf("Reload() error = %v", errReload)
		}
	}
	wg.Wait()
	close(errCh)
	for message := range errCh {
		t.Fatal(message)
	}
	got := store.Snapshot()
	if len(got.ModelAllowlist) != 1 || got.ModelAllowlist[0] != "model-b" {
		t.Fatalf("stored snapshot was mutated by a reader: %#v", got.ModelAllowlist)
	}
}

func TestParseRejectsInvalidKnownFields(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "root type", raw: "- not-a-map\n"},
		{name: "boolean type", raw: "rtk_enabled: \"true\"\n"},
		{name: "caveman enum", raw: "caveman_level: verbose\n"},
		{name: "ponytail enum", raw: "ponytail_level: wenyan\n"},
		{name: "timeout type", raw: "headroom_timeout_ms: \"1500\"\n"},
		{name: "timeout below minimum", raw: "headroom_timeout_ms: 99\n"},
		{name: "timeout above maximum", raw: "headroom_timeout_ms: 1501\n"},
		{name: "https URL", raw: "headroom_url: https://127.0.0.1:8787\n"},
		{name: "hostname URL", raw: "headroom_url: http://localhost:8787\n"},
		{name: "non-loopback URL", raw: "headroom_url: http://127.0.0.2:8787\n"},
		{name: "userinfo URL", raw: "headroom_url: http://user@127.0.0.1:8787\n"},
		{name: "custom path URL", raw: "headroom_url: http://127.0.0.1:8787/custom\n"},
		{name: "query URL", raw: "headroom_url: http://127.0.0.1:8787?x=1\n"},
		{name: "fragment URL", raw: "headroom_url: http://127.0.0.1:8787#fragment\n"},
		{name: "allowlist scalar", raw: "model_allowlist: model-a\n"},
		{name: "allowlist item type", raw: "model_allowlist: [model-a, 7]\n"},
		{name: "allowlist blank item", raw: "model_allowlist: [model-a, \"  \"]\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(tt.raw)); err == nil {
				t.Fatalf("Parse(%q) error = nil", tt.raw)
			}
		})
	}
}
