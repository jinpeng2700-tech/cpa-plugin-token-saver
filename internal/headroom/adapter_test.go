package headroom

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/cpa-plugin-token-saver/internal/protocol"
)

func TestAdapterOpenAIChatSuccessPreservesNonTargetFields(t *testing.T) {
	t.Parallel()

	body := []byte(`{
  "model":"gpt-test",
  "messages":[
    {"role":"system","content":"never change system"},
    {"role":"user","content":[{"type":"text","text":"long user text"}]},
    {"role":"assistant","content":"assistant history","tool_calls":[{"id":"call_1","type":"function","function":{"name":"run","arguments":"{\"x\":1}"}}]},
    {"role":"tool","tool_call_id":"call_1","content":"very long tool output","name":"run"}
  ],
  "tools":[{"type":"function","function":{"name":"run","parameters":{"type":"object"}}}],
  "temperature":0.25
}`)

	adapter, closeServer := adapterWithHandler(t, func(writer http.ResponseWriter, request *http.Request) {
		messages := readWireMessages(t, request)
		setMessageContent(t, messages[1], 0, "short user")
		setMessageStringContent(t, messages[2], "short assistant")
		setMessageStringContent(t, messages[3], "short tool")
		writeMessages(t, writer, messages)
	})
	defer closeServer()

	output, outcome := adapter.Apply(context.Background(), body, protocol.Pair{From: "claude", To: "openai"}, "gpt-test")
	if outcome != OutcomeApplied {
		t.Fatalf("outcome = %q", outcome)
	}
	var got map[string]any
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatal(err)
	}
	messages := got["messages"].([]any)
	if content := messages[0].(map[string]any)["content"]; content != "never change system" {
		t.Fatalf("system content = %#v", content)
	}
	if content := messages[3].(map[string]any)["content"]; content != "short tool" {
		t.Fatalf("tool content = %#v", content)
	}
	if id := messages[3].(map[string]any)["tool_call_id"]; id != "call_1" {
		t.Fatalf("tool_call_id = %#v", id)
	}
	if got["temperature"] != 0.25 || got["tools"] == nil {
		t.Fatalf("non-target fields changed: %#v", got)
	}
}

func TestAdapterResponsesMessageOnlySuccess(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"codex-test","input":[{"type":"message","role":"system","content":[{"type":"input_text","text":"keep"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"long"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"history"}],"status":"completed"}],"parallel_tool_calls":true}`)
	adapter, closeServer := adapterWithHandler(t, func(writer http.ResponseWriter, request *http.Request) {
		messages := readWireMessages(t, request)
		setMessageContent(t, messages[1], 0, "short")
		setMessageContent(t, messages[2], 0, "brief")
		writeMessages(t, writer, messages)
	})
	defer closeServer()

	output, outcome := adapter.Apply(context.Background(), body, protocol.Pair{From: "openai", To: "codex"}, "codex-test")
	if outcome != OutcomeApplied {
		t.Fatalf("outcome = %q", outcome)
	}
	var got struct {
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Status  string `json:"status"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatal(err)
	}
	if got.Input[0].Content[0].Text != "keep" || got.Input[1].Content[0].Text != "short" || got.Input[2].Content[0].Text != "brief" {
		t.Fatalf("unexpected texts: %#v", got.Input)
	}
	if got.Input[2].Content[0].Type != "output_text" || got.Input[2].Status != "completed" {
		t.Fatalf("Responses invariants changed: %#v", got.Input[2])
	}
}

func TestAdapterClaudeSuccessPreservesSystemToolsAndBlockOrder(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"claude-test","system":[{"type":"text","text":"keep system"}],"messages":[{"role":"assistant","content":[{"type":"text","text":"thinking aloud"},{"type":"tool_use","id":"tool_1","name":"shell","input":{"command":"pwd"}},{"type":"text","text":"after tool"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":[{"type":"text","text":"long result"}]},{"type":"text","text":"continue"}]}],"max_tokens":2048}`)
	adapter, closeServer := adapterWithHandler(t, func(writer http.ResponseWriter, request *http.Request) {
		messages := readWireMessages(t, request)
		// Projection order: system, assistant text, tool call, assistant text,
		// tool result, user text.
		setMessageStringContent(t, messages[1], "brief thought")
		setMessageStringContent(t, messages[3], "after")
		setMessageContent(t, messages[4], 0, "short result")
		setMessageStringContent(t, messages[5], "go on")
		writeMessages(t, writer, messages)
	})
	defer closeServer()

	output, outcome := adapter.Apply(context.Background(), body, protocol.Pair{From: "openai", To: "claude"}, "claude-test")
	if outcome != OutcomeApplied {
		t.Fatalf("outcome = %q", outcome)
	}
	var got map[string]any
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatal(err)
	}
	if got["max_tokens"] != float64(2048) {
		t.Fatalf("max_tokens changed: %#v", got["max_tokens"])
	}
	if system := got["system"].([]any)[0].(map[string]any)["text"]; system != "keep system" {
		t.Fatalf("system = %#v", system)
	}
	assistant := got["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if assistant[0].(map[string]any)["text"] != "brief thought" || assistant[1].(map[string]any)["type"] != "tool_use" || assistant[1].(map[string]any)["id"] != "tool_1" || assistant[2].(map[string]any)["text"] != "after" {
		t.Fatalf("assistant blocks changed: %#v", assistant)
	}
	user := got["messages"].([]any)[1].(map[string]any)["content"].([]any)
	result := user[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if result["text"] != "short result" || user[0].(map[string]any)["tool_use_id"] != "tool_1" || user[1].(map[string]any)["text"] != "go on" {
		t.Fatalf("user blocks changed: %#v", user)
	}
}

func TestAdapterBypassesUnsupportedPayloadsByteIdenticallyWithoutNetwork(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	adapter, closeServer := adapterWithHandler(t, func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	})
	defer closeServer()

	tests := []struct {
		name string
		pair protocol.Pair
		body []byte
		want Outcome
	}{
		{name: "gemini", pair: protocol.Pair{From: "openai", To: "gemini"}, body: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`), want: OutcomeUnsupportedFormat},
		{name: "unknown pair", pair: protocol.Pair{From: "plugin", To: "openai"}, body: []byte(`{"messages":[{"role":"user","content":"hello"}]}`), want: OutcomeUnsupportedFormat},
		{name: "responses tool", pair: protocol.Pair{From: "openai", To: "codex"}, body: []byte(`{"input":[{"type":"function_call_output","call_id":"c1","output":"x"}]}`), want: OutcomeUnsupportedStructure},
		{name: "responses reasoning", pair: protocol.Pair{From: "openai", To: "codex"}, body: []byte(`{"input":[{"type":"reasoning","summary":[]}]}`), want: OutcomeUnsupportedStructure},
		{name: "responses image", pair: protocol.Pair{From: "openai", To: "codex"}, body: []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:x"}]}]}`), want: OutcomeUnsupportedStructure},
		{name: "chat image", pair: protocol.Pair{From: "openai", To: "openai"}, body: []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:x"}}]}]}`), want: OutcomeUnsupportedStructure},
		{name: "claude image", pair: protocol.Pair{From: "openai", To: "claude"}, body: []byte(`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"x"}}]}]}`), want: OutcomeUnsupportedStructure},
		{name: "missing messages", pair: protocol.Pair{From: "openai", To: "openai"}, body: []byte(`{"model":"gpt"}`), want: OutcomeUnsupportedStructure},
		{name: "malformed", pair: protocol.Pair{From: "openai", To: "openai"}, body: []byte(`{"messages":`), want: OutcomeUnsupportedStructure},
		{name: "duplicate field", pair: protocol.Pair{From: "openai", To: "openai"}, body: []byte(`{"messages":[],"messages":[{"role":"user","content":"hello"}]}`), want: OutcomeUnsupportedStructure},
		{name: "empty", pair: protocol.Pair{From: "openai", To: "openai"}, body: nil, want: OutcomeUnsupportedStructure},
		{name: "oversized", pair: protocol.Pair{From: "openai", To: "openai"}, body: []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("x", maxPayloadBytes) + `"}]}`), want: OutcomeRequestTooLarge},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, outcome := adapter.Apply(context.Background(), test.body, test.pair, "model")
			if outcome != test.want {
				t.Fatalf("outcome = %q, want %q", outcome, test.want)
			}
			if string(output) != string(test.body) {
				t.Fatalf("bypass changed body\nwant: %s\n got: %s", test.body, output)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("Headroom received %d bypassed requests", calls.Load())
	}
}

func TestAdapterRejectsResponseInvariantViolationsByteIdentically(t *testing.T) {
	t.Parallel()

	body := []byte(`{"messages":[{"role":"system","content":"system"},{"role":"assistant","content":"before","tool_calls":[{"id":"call_1","type":"function","function":{"name":"run","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"result"}]}]}`)
	tests := []struct {
		name   string
		mutate func([]any) []any
	}{
		{name: "delete system", mutate: func(messages []any) []any { return messages[1:] }},
		{name: "add system", mutate: func(messages []any) []any {
			return append(messages, map[string]any{"role": "system", "content": "new"})
		}},
		{name: "change system", mutate: func(messages []any) []any { messages[0].(map[string]any)["content"] = "changed"; return messages }},
		{name: "change role", mutate: func(messages []any) []any { messages[2].(map[string]any)["role"] = "user"; return messages }},
		{name: "change tool id", mutate: func(messages []any) []any { messages[2].(map[string]any)["tool_call_id"] = "call_2"; return messages }},
		{name: "change call id", mutate: func(messages []any) []any {
			messages[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["id"] = "call_2"
			return messages
		}},
		{name: "change block type", mutate: func(messages []any) []any {
			messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)["type"] = "image_url"
			return messages
		}},
		{name: "reorder", mutate: func(messages []any) []any { messages[1], messages[2] = messages[2], messages[1]; return messages }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, closeServer := adapterWithHandler(t, func(writer http.ResponseWriter, request *http.Request) {
				messages := test.mutate(readWireMessages(t, request))
				writeMessages(t, writer, messages)
			})
			defer closeServer()
			output, outcome := adapter.Apply(context.Background(), body, protocol.Pair{From: "openai", To: "openai"}, "model")
			if outcome != OutcomeInvalidResponse {
				t.Fatalf("outcome = %q", outcome)
			}
			if string(output) != string(body) {
				t.Fatalf("invalid response changed body: %s", output)
			}
		})
	}
}

func TestAdapterReturnsStageInputOnHTTPAndJSONFailures(t *testing.T) {
	t.Parallel()

	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	tests := []struct {
		name     string
		status   int
		encoding string
		body     string
		want     Outcome
	}{
		{name: "non 2xx", status: http.StatusBadGateway, body: `{}`, want: OutcomeHTTPStatus},
		{name: "invalid json", status: http.StatusOK, body: `{`, want: OutcomeInvalidJSON},
		{name: "missing messages", status: http.StatusOK, body: `{}`, want: OutcomeInvalidResponse},
		{name: "gzip", status: http.StatusOK, encoding: "gzip", body: `x`, want: OutcomeResponseEncoding},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, closeServer := adapterWithHandler(t, func(writer http.ResponseWriter, _ *http.Request) {
				if test.encoding != "" {
					writer.Header().Set("Content-Encoding", test.encoding)
				}
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, test.body)
			})
			defer closeServer()
			output, outcome := adapter.Apply(context.Background(), body, protocol.Pair{From: "openai", To: "openai"}, "model")
			if outcome != test.want {
				t.Fatalf("outcome = %q, want %q", outcome, test.want)
			}
			if string(output) != string(body) {
				t.Fatalf("failure changed body: %s", output)
			}
		})
	}
}

func TestAdapterNoChangeKeepsOriginalBytes(t *testing.T) {
	t.Parallel()

	body := []byte("{\n  \"messages\": [ { \"role\": \"user\", \"content\": \"unchanged\" } ],\n  \"temperature\": 1.0\n}")
	adapter, closeServer := adapterWithHandler(t, func(writer http.ResponseWriter, request *http.Request) {
		writeMessages(t, writer, readWireMessages(t, request))
	})
	defer closeServer()
	output, outcome := adapter.Apply(context.Background(), body, protocol.Pair{From: "openai", To: "openai"}, "model")
	if outcome != OutcomeNoChange {
		t.Fatalf("outcome = %q", outcome)
	}
	if string(output) != string(body) {
		t.Fatalf("no-change response rewrote bytes: %s", output)
	}
}

func TestAdapterRejectsRewriteThatExceedsPayloadLimit(t *testing.T) {
	t.Parallel()

	body := []byte(`{"messages":[{"role":"user","content":"long"}],"opaque":"` + strings.Repeat("x", 850*1024) + `"}`)
	adapter, closeServer := adapterWithHandler(t, func(writer http.ResponseWriter, request *http.Request) {
		messages := readWireMessages(t, request)
		setMessageStringContent(t, messages[0], strings.Repeat("y", 250*1024))
		writeMessages(t, writer, messages)
	})
	defer closeServer()

	output, outcome := adapter.Apply(context.Background(), body, protocol.Pair{From: "openai", To: "openai"}, "model")
	if outcome != OutcomeResponseTooLarge {
		t.Fatalf("outcome = %q", outcome)
	}
	if string(output) != string(body) {
		t.Fatal("oversized rewrite changed the stage input")
	}
}

func adapterWithHandler(t *testing.T, handler http.HandlerFunc) (*Adapter, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	client := mustClient(t, server.URL, time.Second)
	return NewAdapter(client), func() {
		client.CloseIdleConnections()
		server.Close()
	}
}

func readWireMessages(t *testing.T, request *http.Request) []any {
	t.Helper()
	var payload struct {
		Messages []any  `json:"messages"`
		Model    string `json:"model"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Model == "" || payload.Messages == nil {
		t.Fatalf("invalid request payload: %#v", payload)
	}
	return payload.Messages
}

func writeMessages(t *testing.T, writer http.ResponseWriter, messages []any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(map[string]any{"messages": messages, "tokens_saved": 1}); err != nil {
		t.Fatal(err)
	}
}

func setMessageStringContent(t *testing.T, message any, value string) {
	t.Helper()
	object, ok := message.(map[string]any)
	if !ok {
		t.Fatalf("message = %#v", message)
	}
	object["content"] = value
}

func setMessageContent(t *testing.T, message any, index int, value string) {
	t.Helper()
	object, ok := message.(map[string]any)
	if !ok {
		t.Fatalf("message = %#v", message)
	}
	blocks, ok := object["content"].([]any)
	if !ok || index >= len(blocks) {
		t.Fatalf("content = %#v", object["content"])
	}
	block, ok := blocks[index].(map[string]any)
	if !ok {
		t.Fatalf("block = %#v", blocks[index])
	}
	block["text"] = value
}
