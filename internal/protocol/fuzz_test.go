package protocol

import (
	"encoding/json"
	"testing"
)

func FuzzViewRewrite(f *testing.F) {
	f.Add(byte(0), []byte(`{"messages":[{"role":"tool","tool_call_id":"call_1","content":"result"}]}`))
	f.Add(byte(1), []byte(`{"input":[{"type":"function_call_output","call_id":"call_1","output":"result"}]}`))
	f.Add(byte(2), []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"result"}]}]}`))
	f.Add(byte(3), []byte(`{"contents":[{"role":"user","parts":[{"functionResponse":{"id":"call_1","response":{"result":"result"}}}]}]}`))

	pairs := []Pair{
		{From: "openai", To: "openai"},
		{From: "openai-response", To: "codex"},
		{From: "openai", To: "claude"},
		{From: "openai", To: "gemini"},
		{From: "plugin-format", To: "openai"},
	}
	f.Fuzz(func(t *testing.T, selector byte, body []byte) {
		pair := pairs[int(selector)%len(pairs)]
		view, ok := View(body, pair)
		if !ok {
			return
		}
		before := view.Slots()
		replacements := make(map[int]string, len(before))
		for index := range before {
			replacements[index] = "fuzz\nreplacement"
		}
		rewritten := view.Rewrite(replacements)
		if !json.Valid(rewritten) {
			t.Fatalf("Rewrite produced invalid JSON: %q", rewritten)
		}
		afterView, afterOK := View(rewritten, pair)
		if !afterOK {
			t.Fatal("Rewrite changed a recognized payload into an unrecognized payload")
		}
		after := afterView.Slots()
		if len(after) != len(before) {
			t.Fatalf("slot count changed from %d to %d", len(before), len(after))
		}
		for index := range before {
			if after[index].ResultID != before[index].ResultID || after[index].Kind != before[index].Kind || after[index].Error != before[index].Error {
				t.Fatalf("slot identity changed at %d: before=%#v after=%#v", index, before[index], after[index])
			}
		}
	})
}
