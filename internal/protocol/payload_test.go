package protocol

import (
	"bytes"
	"testing"
)

func TestViewFindsOnlyProviderToolResultTextSlots(t *testing.T) {
	tests := []struct {
		name    string
		pair    Pair
		body    string
		wantIDs []string
		want    []string
	}{
		{
			name:    "openai chat",
			pair:    Pair{From: "openai", To: "openai"},
			body:    `{"model":"m","messages":[{"role":"assistant","content":"ordinary"},{"role":"tool","tool_call_id":"call_1","content":"one"},{"role":"tool","tool_call_id":"call_2","content":[{"type":"text","text":"two"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AQID"}}]}]}`,
			wantIDs: []string{"call_1", "call_2"},
			want:    []string{"one", "two"},
		},
		{
			name:    "openai responses",
			pair:    Pair{From: "openai-response", To: "codex"},
			body:    `{"model":"m","input":[{"role":"user","content":"ordinary"},{"type":"function_call_output","call_id":"call_1","output":"one"},{"type":"function_call_output","call_id":"call_2","output":[{"type":"input_text","text":"two"},{"type":"input_image","image_url":"data:image/png;base64,AQID"}]}]}`,
			wantIDs: []string{"call_1", "call_2"},
			want:    []string{"one", "two"},
		},
		{
			name:    "claude messages",
			pair:    Pair{From: "openai", To: "claude"},
			body:    `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"ordinary"},{"type":"tool_result","tool_use_id":"toolu_1","content":"one"},{"type":"tool_result","tool_use_id":"toolu_2","content":[{"type":"text","text":"two"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AQID"}}]}]}]}`,
			wantIDs: []string{"toolu_1", "toolu_2"},
			want:    []string{"one", "two"},
		},
		{
			name:    "gemini function response",
			pair:    Pair{From: "openai", To: "gemini"},
			body:    `{"contents":[{"role":"user","parts":[{"text":"ordinary"},{"functionResponse":{"id":"call_1","name":"run","response":{"result":"one","count":9007199254740993},"parts":[{"inlineData":{"mimeType":"image/png","data":"AQID"}}]}}]}]}`,
			wantIDs: []string{"call_1"},
			want:    []string{"one"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view, ok := View([]byte(tt.body), tt.pair)
			if !ok {
				t.Fatal("View() did not recognize provider message payload")
			}
			slots := view.Slots()
			if len(slots) != len(tt.want) {
				t.Fatalf("len(Slots()) = %d, want %d: %#v", len(slots), len(tt.want), slots)
			}
			for i := range slots {
				if slots[i].Text != tt.want[i] || slots[i].ResultID != tt.wantIDs[i] {
					t.Fatalf("slot[%d] = %#v, want text=%q id=%q", i, slots[i], tt.want[i], tt.wantIDs[i])
				}
			}

			got := view.Rewrite(map[int]string{0: "compact"})
			if !bytes.Contains(got, []byte(`"compact"`)) || bytes.Contains(got, []byte(`"one"`)) {
				t.Fatalf("Rewrite() did not replace the selected text slot: %s", got)
			}
			for _, opaque := range []string{"ordinary", "AQID"} {
				if !bytes.Contains(got, []byte(opaque)) {
					t.Fatalf("Rewrite() lost opaque data %q: %s", opaque, got)
				}
			}
		})
	}
}

func TestViewFindsCodexCustomToolOutputTextSlots(t *testing.T) {
	body := []byte(`{"input":[{"type":"custom_tool_call_output","call_id":"call_1","output":[{"type":"input_text","text":"meta"},{"type":"output_text","text":"body"},{"type":"input_image","image_url":"data:image/png;base64,AQID"}]},{"type":"function_call_output","call_id":"call_2","output":"function"},{"type":"local_shell_call_output","call_id":"call_3","output":"shell"},{"type":"apply_patch_call_output","call_id":"call_4","output":"patch"}]}`)
	view, ok := View(body, Pair{From: "openai-response", To: "codex"})
	if !ok {
		t.Fatal("View() did not recognize Codex Responses payload")
	}
	slots := view.Slots()
	if len(slots) != 5 || slots[0].Text != "meta" || slots[1].Text != "body" || slots[2].Text != "function" || slots[3].Text != "shell" || slots[4].Text != "patch" {
		t.Fatalf("slots = %#v", slots)
	}
	got := view.Rewrite(map[int]string{1: "compact"})
	want := []byte(`{"input":[{"type":"custom_tool_call_output","call_id":"call_1","output":[{"type":"input_text","text":"meta"},{"type":"output_text","text":"compact"},{"type":"input_image","image_url":"data:image/png;base64,AQID"}]},{"type":"function_call_output","call_id":"call_2","output":"function"},{"type":"local_shell_call_output","call_id":"call_3","output":"shell"},{"type":"apply_patch_call_output","call_id":"call_4","output":"patch"}]}`)
	if !bytes.Equal(got, want) {
		t.Fatalf("Rewrite() changed opaque Codex fields:\n got %s\nwant %s", got, want)
	}
}

func TestViewMarksErrorsAndPreservesBypassBytes(t *testing.T) {
	body := []byte("  {\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"tool_result\",\"tool_use_id\":\"e1\",\"is_error\":true,\"content\":\"failure\"}]}]}  \n")
	view, ok := View(body, Pair{From: "openai", To: "claude"})
	if !ok || len(view.Slots()) != 1 || !view.Slots()[0].Error {
		t.Fatalf("error slot = %#v, recognized=%v", view.Slots(), ok)
	}
	if got := view.Rewrite(nil); !bytes.Equal(got, body) {
		t.Fatalf("empty rewrite changed bytes:\n got %q\nwant %q", got, body)
	}

	for _, tt := range []struct {
		name string
		body []byte
		pair Pair
	}{
		{name: "unknown native pair", body: body, pair: Pair{From: "claude", To: "claude"}},
		{name: "malformed", body: []byte(`{"messages":[`), pair: Pair{From: "openai", To: "openai"}},
		{name: "empty", body: nil, pair: Pair{From: "openai", To: "openai"}},
		{name: "non-message endpoint", body: []byte(`{"model":"gpt-image-1","prompt":"tool-looking text"}`), pair: Pair{From: "openai", To: "openai"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, recognized := View(tt.body, tt.pair); recognized {
				t.Fatal("View() recognized a payload that must bypass")
			}
		})
	}
}

func TestRewriteAppliesMultipleSlotsWithoutReencodingSurroundingBytes(t *testing.T) {
	body := []byte(" {\n  \"messages\": [\n    {\"role\":\"tool\",\"tool_call_id\":\"one\",\"content\":\"first\"},\n    {\"role\":\"tool\",\"tool_call_id\":\"two\",\"content\":\"second\"}\n  ],\n  \"temperature\": 1.0\n}\n")
	view, ok := View(body, Pair{From: "openai", To: "openai"})
	if !ok || len(view.Slots()) != 2 {
		t.Fatalf("slots = %#v, recognized=%v", view.Slots(), ok)
	}

	want := []byte(" {\n  \"messages\": [\n    {\"role\":\"tool\",\"tool_call_id\":\"one\",\"content\":\"x\"},\n    {\"role\":\"tool\",\"tool_call_id\":\"two\",\"content\":\"longer\\nvalue\"}\n  ],\n  \"temperature\": 1.0\n}\n")
	got := view.Rewrite(map[int]string{0: "x", 1: "longer\nvalue"})
	if !bytes.Equal(got, want) {
		t.Fatalf("multi-slot rewrite changed surrounding bytes:\n got %q\nwant %q", got, want)
	}
}

func TestPairEligibilityMatchesBuiltInTranslatorRegistrations(t *testing.T) {
	for _, pair := range []Pair{
		{From: "openai", To: "openai"},
		{From: "openai-response", To: "openai"},
		{From: "openai-response", To: "codex"},
		{From: "openai", To: "claude"},
		{From: "openai", To: "gemini"},
		{From: "openai-response", To: "antigravity"},
		{From: "interactions", To: "openai-response"},
	} {
		if !pair.Eligible() {
			t.Fatalf("Pair %#v should be eligible", pair)
		}
	}
	for _, pair := range []Pair{
		{From: "claude", To: "claude"},
		{From: "plugin-format", To: "claude"},
		{From: "openai", To: "plugin-format"},
		{},
	} {
		if pair.Eligible() {
			t.Fatalf("Pair %#v should bypass", pair)
		}
	}
}
