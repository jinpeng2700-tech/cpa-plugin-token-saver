package abi

import (
	"encoding/json"
	"testing"
)

func FuzzRuntimeCall(f *testing.F) {
	f.Add(MethodPluginRegister, []byte(`{"schema_version":3,"config_yaml":null}`))
	f.Add(MethodPluginReconfigure, []byte(`{"schema_version":3,"config_yaml":"cnRrX2VuYWJsZWQ6IHRydWUK"}`))
	f.Add(MethodRequestNormalize, []byte(`{"FromFormat":"openai","ToFormat":"openai","Model":"m","Body":"eyJtZXNzYWdlcyI6W3sicm9sZSI6InVzZXIiLCJjb250ZW50IjoiaGkifV19"}`))
	f.Add(MethodManagementHandle, []byte(`{"Method":"GET","Path":"/v0/management/plugins/token-saver/status"}`))
	f.Add("unknown", []byte(`{}`))

	f.Fuzz(func(t *testing.T, method string, request []byte) {
		if len(method) > MaxMethodBytes+1 || len(request) > 1<<20 {
			return
		}
		runtimeState := NewRuntime()
		raw, status := runtimeState.Call(method, request)
		runtimeState.Shutdown()

		if status != CallStatusOK && status != CallStatusError {
			t.Fatalf("status = %d", status)
		}
		if !json.Valid(raw) {
			t.Fatalf("runtime returned invalid JSON: %q", raw)
		}
		var envelope Envelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.OK == (envelope.Error != nil) {
			t.Fatalf("invalid envelope success/error state: %#v", envelope)
		}
	})
}
