package prompt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/protocol"
)

const (
	wantCavemanStart  = "[CPA_TOKEN_SAVER_CAVEMAN_START]"
	wantCavemanEnd    = "[CPA_TOKEN_SAVER_CAVEMAN_END]"
	wantPonytailStart = "[CPA_TOKEN_SAVER_PONYTAIL_START]"
	wantPonytailEnd   = "[CPA_TOKEN_SAVER_PONYTAIL_END]"
)

func TestPromptFaceSnapshots(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		level  string
		bytes  int
		sha256 string
		get    func(string) (string, bool)
	}{
		{name: "caveman-lite", level: "lite", bytes: 1736, sha256: "a02d21cacddf2652920354395b2602f4a59d1d614d952bd8248b07667b327f56", get: Caveman},
		{name: "caveman-full", level: "full", bytes: 1834, sha256: "58e2a0acf2dcd05f9d603907f30891b718c359b36d4a27228ae005b9ae5995ca", get: Caveman},
		{name: "caveman-ultra", level: "ultra", bytes: 1678, sha256: "8213b96f394102a656cef64fffb58bf6569fb12fbe8ffcd3c43e0d3585e90b5a", get: Caveman},
		{name: "caveman-wenyan-lite", level: "wenyan-lite", bytes: 1704, sha256: "18ab72a145a80dd9b98ee30986e86c70fdc913c507f3fd684745f753930a112c", get: Caveman},
		{name: "caveman-wenyan", level: "wenyan", bytes: 1809, sha256: "3d19b1bba036d2c54785b24004ba42b9ccc241cca9156ca733577f9a7171881b", get: Caveman},
		{name: "caveman-wenyan-ultra", level: "wenyan-ultra", bytes: 1711, sha256: "e708c17b3a3157b6b28b00f8202a6bc83c15f36cadf89f9c950851e1c31a31e3", get: Caveman},
		{name: "ponytail-lite", level: "lite", bytes: 1899, sha256: "0eafe3886b03bd397cc695aa86a76f24833f757c0ded374faaa3ff14ac02042b", get: Ponytail},
		{name: "ponytail-full", level: "full", bytes: 1905, sha256: "32e031696b08a29f4e1266650d1d908117266fbee7fdba46262aeaac5ff7e6b2", get: Ponytail},
		{name: "ponytail-ultra", level: "ultra", bytes: 1949, sha256: "ccabefd9553ca6576fb04a340eeb78824b765be1f03ead1774f9ee8f4c7ffff4", get: Ponytail},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, ok := test.get(test.level)
			if !ok {
				t.Fatalf("prompt %q is missing", test.level)
			}
			sum := sha256.Sum256([]byte(got))
			gotHash := hex.EncodeToString(sum[:])
			if len([]byte(got)) != test.bytes || gotHash != test.sha256 {
				t.Fatalf("prompt snapshot = %d bytes, SHA-256 %s; want %d bytes, SHA-256 %s", len([]byte(got)), gotHash, test.bytes, test.sha256)
			}
		})
	}

	if _, ok := Caveman("invalid"); ok {
		t.Fatal("invalid Caveman level unexpectedly accepted")
	}
	if _, ok := Ponytail("invalid"); ok {
		t.Fatal("invalid Ponytail level unexpectedly accepted")
	}
}

func TestPonytailTracksOfficial490Rules(t *testing.T) {
	for _, level := range []string{"lite", "full", "ultra"} {
		prompt, ok := Ponytail(level)
		if !ok {
			t.Fatalf("Ponytail(%q) is missing", level)
		}
		for _, want := range []string{
			"Already in this codebase?",
			"trace the real flow end to end",
			"Bug fix = root cause, not symptom",
			"Ponytail governs what you build, not how you talk",
		} {
			if !strings.Contains(prompt, want) {
				t.Errorf("Ponytail(%q) missing %q", level, want)
			}
		}
		if len([]byte(prompt)) > 2048 {
			t.Errorf("Ponytail(%q) = %d bytes, want <= 2048", level, len([]byte(prompt)))
		}
	}
}

func TestInjectSupportedProviderShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		pair         protocol.Pair
		body         string
		originalText string
		injectedText func(t *testing.T, body []byte) string
		preservedRaw []string
	}{
		{
			name:         "openai-chat-system-string",
			pair:         protocol.Pair{From: "openai", To: "openai"},
			body:         `{"model":"gpt","temperature":1e+06,"messages":[{"role":"system","content":"既有 system instruction"},{"role":"user","content":[{"type":"text","text":"Hello 世界"},{"type":"image_url","image_url":{"url":"data:image/png;base64,opaque"}}]}],"opaque":{"keep":true}}`,
			originalText: "既有 system instruction",
			injectedText: openAIInstruction,
			preservedRaw: []string{`"temperature":1e+06`, `{"type":"image_url","image_url":{"url":"data:image/png;base64,opaque"}}`, `"opaque":{"keep":true}`},
		},
		{
			name:         "responses-instructions",
			pair:         protocol.Pair{From: "openai-response", To: "codex"},
			body:         `{"model":"codex","instructions":"Existing developer instruction","input":[{"role":"user","content":[{"type":"input_text","text":"Fix auth"},{"type":"input_image","image_url":"opaque://image"}]},{"type":"function_call","call_id":"call_1","name":"run","arguments":"{}"}],"max_output_tokens":4.20e2}`,
			originalText: "Existing developer instruction",
			injectedText: responsesInstruction,
			preservedRaw: []string{`{"type":"input_image","image_url":"opaque://image"}`, `"max_output_tokens":4.20e2`},
		},
		{
			name:         "claude-system-blocks",
			pair:         protocol.Pair{From: "openai", To: "claude"},
			body:         `{"model":"claude","system":[{"type":"text","text":"Existing Claude system"},{"type":"opaque","data":{"n":1e3}},{"type":"text","text":"cached","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"你好"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"opaque"}}]}]}`,
			originalText: "Existing Claude system",
			injectedText: claudeInstruction,
			preservedRaw: []string{`{"type":"opaque","data":{"n":1e3}}`, `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"opaque"}}`},
		},
		{
			name:         "gemini-snake-system-instruction",
			pair:         protocol.Pair{From: "openai", To: "gemini"},
			body:         `{"model":"gemini","system_instruction":{"parts":[{"text":"Existing Gemini system"},{"inlineData":{"mimeType":"image/png","data":"opaque"}}]},"contents":[{"role":"user","parts":[{"text":"Hello 世界"},{"fileData":{"mimeType":"image/png","fileUri":"opaque://image"}}]}],"generationConfig":{"temperature":1.00}}`,
			originalText: "Existing Gemini system",
			injectedText: geminiInstruction,
			preservedRaw: []string{`{"inlineData":{"mimeType":"image/png","data":"opaque"}}`, `{"fileData":{"mimeType":"image/png","fileUri":"opaque://image"}}`, `"temperature":1.00`},
		},
		{
			name:         "antigravity-system-instruction",
			pair:         protocol.Pair{From: "openai-response", To: "antigravity"},
			body:         `{"project":"project-1","request":{"systemInstruction":{"parts":[{"text":"Existing Antigravity system"},{"opaque":{"n":1e3}}]},"contents":[{"role":"user","parts":[{"text":"Hello world"},{"fileData":{"mimeType":"image/png","fileUri":"opaque://image"}}]}],"tools":[{"functionDeclarations":[{"name":"run","parametersJsonSchema":{"type":"object"}}]}]},"model":"gemini-3.7-flash-high","opaque":{"keep":true}}`,
			originalText: "Existing Antigravity system",
			injectedText: antigravityInstruction,
			preservedRaw: []string{`{"opaque":{"n":1e3}}`, `{"fileData":{"mimeType":"image/png","fileUri":"opaque://image"}}`, `"project":"project-1"`, `"opaque":{"keep":true}`},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := Inject([]byte(test.body), test.pair, Options{CavemanLevel: "full", PonytailLevel: "full"})
			if bytes.Equal(got, []byte(test.body)) {
				t.Fatal("supported payload was not injected")
			}
			text := test.injectedText(t, got)
			assertInjected(t, text, test.originalText, true, true)
			for _, raw := range test.preservedRaw {
				if !bytes.Contains(got, []byte(raw)) {
					t.Errorf("opaque/raw field changed or disappeared: %s", raw)
				}
			}
		})
	}
}

func TestInjectCreatesLegalSystemLocations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pair protocol.Pair
		body string
		get  func(t *testing.T, body []byte) string
	}{
		{name: "openai", pair: protocol.Pair{From: "claude", To: "openai"}, body: `{"messages":[{"role":"user","content":"hello"}]}`, get: openAIInstruction},
		{name: "responses", pair: protocol.Pair{From: "openai", To: "codex"}, body: `{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`, get: responsesInstruction},
		{name: "claude", pair: protocol.Pair{From: "gemini", To: "claude"}, body: `{"messages":[{"role":"user","content":"hello"}]}`, get: claudeInstruction},
		{name: "gemini", pair: protocol.Pair{From: "claude", To: "gemini"}, body: `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`, get: geminiInstruction},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := Inject([]byte(test.body), test.pair, Options{CavemanLevel: "lite", PonytailLevel: "ultra"})
			assertInjected(t, test.get(t, got), "", true, true)
		})
	}
}

func TestInjectRootFieldPreservesSurroundingWhitespace(t *testing.T) {
	t.Parallel()

	body := []byte(" \n{\"input\":[{\"role\":\"user\",\"content\":\"hello\"}]} \r\n")
	got := Inject(body, protocol.Pair{From: "openai", To: "codex"}, Options{CavemanLevel: "lite"})
	if !json.Valid(bytes.TrimSpace(got)) {
		t.Fatalf("injected payload is invalid JSON: %q", got)
	}
	if !bytes.HasPrefix(got, []byte(" \n")) || !bytes.HasSuffix(got, []byte(" \r\n")) {
		t.Fatalf("surrounding whitespace changed: %q", got)
	}
	if !strings.Contains(responsesInstruction(t, got), wantCavemanStart) {
		t.Fatal("Caveman instruction was not added")
	}
}

func TestInjectAppendsToDeveloperMessages(t *testing.T) {
	t.Parallel()

	chat := []byte(`{"messages":[{"role":"developer","content":[{"type":"text","text":"keep developer"},{"type":"opaque","value":7.00}]},{"role":"user","content":"hello"}]}`)
	gotChat := Inject(chat, protocol.Pair{From: "openai", To: "openai"}, Options{CavemanLevel: "lite"})
	text := openAIInstruction(t, gotChat)
	if !strings.Contains(text, "keep developer") || !strings.Contains(text, wantCavemanStart) {
		t.Fatalf("developer instruction was not appended: %s", text)
	}
	if !bytes.Contains(gotChat, []byte(`{"type":"opaque","value":7.00}`)) {
		t.Fatal("opaque developer block changed")
	}

	responses := []byte(`{"input":[{"role":"developer","content":"keep responses developer"},{"role":"user","content":"hello"}]}`)
	gotResponses := Inject(responses, protocol.Pair{From: "openai", To: "codex"}, Options{PonytailLevel: "lite"})
	text = responsesInstruction(t, gotResponses)
	if !strings.Contains(text, "keep responses developer") || !strings.Contains(text, wantPonytailStart) {
		t.Fatalf("Responses developer instruction was not appended: %s", text)
	}
}

func TestInjectSupportsResponsesLiteAdditionalToolsItem(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-5.6-luna","input":[{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"shell"}]},{"type":"message","role":"developer","content":[{"type":"input_text","text":"keep developer"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	got := Inject(body, protocol.Pair{From: "openai", To: "codex"}, Options{CavemanLevel: "lite", PonytailLevel: "ultra"})

	if bytes.Equal(got, body) {
		t.Fatal("Responses Lite payload was not injected")
	}
	text := responsesInstruction(t, got)
	if !strings.Contains(text, "keep developer") {
		t.Fatal("existing developer instruction disappeared")
	}
	assertMarkerCount(t, text, wantCavemanStart, wantCavemanEnd, true)
	assertMarkerCount(t, text, wantPonytailStart, wantPonytailEnd, true)
	if !bytes.Contains(got, []byte(`{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"shell"}]}`)) {
		t.Fatal("additional_tools item changed")
	}
}

func TestInjectAntigravityIsIdempotent(t *testing.T) {
	t.Parallel()

	body := []byte(`{"project":"project-1","request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]},"model":"gemini-3.7-flash-high"}`)
	pair := protocol.Pair{From: "openai-response", To: "antigravity"}
	options := Options{CavemanLevel: "lite", PonytailLevel: "ultra"}
	first := Inject(body, pair, options)
	second := Inject(first, pair, options)

	if bytes.Equal(first, body) {
		t.Fatal("Antigravity payload was not injected")
	}
	if !bytes.Equal(second, first) {
		t.Fatal("Antigravity injection was not idempotent")
	}
}

func TestInjectSwitchCombinationsAndIdempotency(t *testing.T) {
	t.Parallel()

	body := []byte(`{"messages":[{"role":"system","content":"System"},{"role":"user","content":"First turn"},{"role":"assistant","content":"Reply"},{"role":"user","content":"第二轮"}]}`)
	pair := protocol.Pair{From: "openai", To: "openai"}
	tests := []struct {
		name     string
		options  Options
		caveman  bool
		ponytail bool
	}{
		{name: "both-off"},
		{name: "caveman-only", options: Options{CavemanLevel: "wenyan"}, caveman: true},
		{name: "ponytail-only", options: Options{PonytailLevel: "lite"}, ponytail: true},
		{name: "both-on", options: Options{CavemanLevel: "full", PonytailLevel: "ultra"}, caveman: true, ponytail: true},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			first := Inject(body, pair, test.options)
			second := Inject(first, pair, test.options)
			if !bytes.Equal(first, second) {
				t.Fatalf("second injection changed payload\nfirst:  %s\nsecond: %s", first, second)
			}
			if !test.caveman && !test.ponytail && !bytes.Equal(first, body) {
				t.Fatal("all-off injection was not byte-identical")
			}
			text := openAIInstruction(t, first)
			assertMarkerCount(t, text, wantCavemanStart, wantCavemanEnd, test.caveman)
			assertMarkerCount(t, text, wantPonytailStart, wantPonytailEnd, test.ponytail)
			if test.caveman && test.ponytail && strings.Index(text, wantCavemanStart) >= strings.Index(text, wantPonytailStart) {
				t.Fatal("Caveman must appear before Ponytail")
			}
			for _, original := range []string{"System", "First turn", "Reply", "第二轮"} {
				if !bytes.Contains(first, []byte(original)) {
					t.Errorf("original multi-turn text %q was not preserved", original)
				}
			}
		})
	}
}

func TestInjectCompletesMissingStageInCanonicalOrder(t *testing.T) {
	t.Parallel()

	ponytail, _ := Ponytail("full")
	ponytailOnly := openAISystemBody("System\n\n" + wrapPonytail(ponytail))
	withCaveman := Inject(ponytailOnly, protocol.Pair{From: "openai", To: "openai"}, Options{CavemanLevel: "full"})
	text := openAIInstruction(t, withCaveman)
	assertInjected(t, text, "System", true, true)
	if strings.Index(text, wantCavemanStart) >= strings.Index(text, wantPonytailStart) {
		t.Fatal("late Caveman completion was not inserted before existing Ponytail")
	}

	caveman, _ := Caveman("full")
	cavemanOnly := openAISystemBody("System\n\n" + wrapCaveman(caveman))
	withPonytail := Inject(cavemanOnly, protocol.Pair{From: "openai", To: "openai"}, Options{PonytailLevel: "full"})
	text = openAIInstruction(t, withPonytail)
	assertInjected(t, text, "System", true, true)
	if strings.Index(text, wantCavemanStart) >= strings.Index(text, wantPonytailStart) {
		t.Fatal("late Ponytail completion did not follow existing Caveman")
	}
}

func TestInjectMalformedMarkersBypassOnlyTheirStage(t *testing.T) {
	t.Parallel()

	body := []byte(`{"messages":[{"role":"system","content":"System ` + wantCavemanStart + ` partial"},{"role":"user","content":"hello"}]}`)
	got := Inject(body, protocol.Pair{From: "openai", To: "openai"}, Options{CavemanLevel: "full", PonytailLevel: "full"})
	text := openAIInstruction(t, got)
	if strings.Count(text, wantCavemanStart) != 1 || strings.Count(text, wantCavemanEnd) != 0 {
		t.Fatal("malformed Caveman marker was modified or duplicated")
	}
	assertMarkerCount(t, text, wantPonytailStart, wantPonytailEnd, true)

	duplicate := []byte(`{"messages":[{"role":"system","content":"` + wantPonytailStart + ` x ` + wantPonytailEnd + ` ` + wantPonytailStart + ` y ` + wantPonytailEnd + `"},{"role":"user","content":"hello"}]}`)
	if got := Inject(duplicate, protocol.Pair{From: "openai", To: "openai"}, Options{PonytailLevel: "ultra"}); !bytes.Equal(got, duplicate) {
		t.Fatal("duplicate Ponytail markers must conservatively bypass")
	}
}

func TestInjectBypassesIneligibleOrUnsupportedPayloads(t *testing.T) {
	t.Parallel()

	validChat := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	tests := []struct {
		name string
		pair protocol.Pair
		body []byte
	}{
		{name: "unknown-pair", pair: protocol.Pair{From: "plugin-format", To: "openai"}, body: validChat},
		{name: "malformed-json", pair: protocol.Pair{From: "openai", To: "openai"}, body: []byte(`{"messages":[`)},
		{name: "empty", pair: protocol.Pair{From: "openai", To: "openai"}, body: nil},
		{name: "non-message-image-endpoint", pair: protocol.Pair{From: "openai", To: "openai"}, body: []byte(`{"prompt":"draw a cat","n":1}`)},
		{name: "non-message-responses", pair: protocol.Pair{From: "openai", To: "codex"}, body: []byte(`{"prompt":"not responses input"}`)},
		{name: "unsupported-chat-shape", pair: protocol.Pair{From: "openai", To: "openai"}, body: []byte(`{"messages":"not-an-array"}`)},
		{name: "unknown-chat-message", pair: protocol.Pair{From: "openai", To: "openai"}, body: []byte(`{"messages":[{"content":"missing role"}]}`)},
		{name: "unsupported-responses-shape", pair: protocol.Pair{From: "openai", To: "codex"}, body: []byte(`{"instructions":7,"input":[]}`)},
		{name: "unknown-responses-item", pair: protocol.Pair{From: "openai", To: "codex"}, body: []byte(`{"input":[7]}`)},
		{name: "unsupported-claude-system", pair: protocol.Pair{From: "openai", To: "claude"}, body: []byte(`{"system":7,"messages":[]}`)},
		{name: "unknown-claude-message", pair: protocol.Pair{From: "openai", To: "claude"}, body: []byte(`{"messages":[{"role":"bogus","content":"hello"}]}`)},
		{name: "ambiguous-gemini-system", pair: protocol.Pair{From: "openai", To: "gemini"}, body: []byte(`{"systemInstruction":{"parts":[]},"system_instruction":{"parts":[]},"contents":[]}`)},
		{name: "unknown-gemini-content", pair: protocol.Pair{From: "openai", To: "gemini"}, body: []byte(`{"contents":[{"role":"user","parts":"not-an-array"}]}`)},
		{name: "invalid-caveman-level", pair: protocol.Pair{From: "openai", To: "openai"}, body: validChat},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			options := Options{CavemanLevel: "full", PonytailLevel: "full"}
			if test.name == "invalid-caveman-level" {
				options = Options{CavemanLevel: "not-a-level"}
			}
			got := Inject(test.body, test.pair, options)
			if !bytes.Equal(got, test.body) {
				t.Fatalf("unsupported payload changed\nwant: %s\ngot:  %s", test.body, got)
			}
		})
	}
}

func assertInjected(t *testing.T, text, original string, caveman, ponytail bool) {
	t.Helper()
	if original != "" && !strings.Contains(text, original) {
		t.Errorf("original instruction %q was overwritten: %s", original, text)
	}
	assertMarkerCount(t, text, wantCavemanStart, wantCavemanEnd, caveman)
	assertMarkerCount(t, text, wantPonytailStart, wantPonytailEnd, ponytail)
	if caveman && ponytail && strings.Index(text, wantCavemanStart) >= strings.Index(text, wantPonytailStart) {
		t.Error("Caveman does not precede Ponytail")
	}
}

func assertMarkerCount(t *testing.T, text, start, end string, want bool) {
	t.Helper()
	wantCount := 0
	if want {
		wantCount = 1
	}
	if got := strings.Count(text, start); got != wantCount {
		t.Errorf("start marker count = %d, want %d", got, wantCount)
	}
	if got := strings.Count(text, end); got != wantCount {
		t.Errorf("end marker count = %d, want %d", got, wantCount)
	}
}

func openAIInstruction(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode OpenAI payload: %v", err)
	}
	var result strings.Builder
	for _, message := range payload.Messages {
		if message.Role != "system" && message.Role != "developer" {
			continue
		}
		var text string
		if json.Unmarshal(message.Content, &text) == nil {
			result.WriteString(text)
			continue
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(message.Content, &parts) == nil {
			for _, part := range parts {
				result.WriteString(part.Text)
			}
		}
	}
	return result.String()
}

func responsesInstruction(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Instructions string `json:"instructions"`
		Input        []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode Responses payload: %v", err)
	}
	var result strings.Builder
	result.WriteString(payload.Instructions)
	for _, message := range payload.Input {
		if message.Role != "system" && message.Role != "developer" {
			continue
		}
		var text string
		if json.Unmarshal(message.Content, &text) == nil {
			result.WriteString(text)
			continue
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(message.Content, &parts) == nil {
			for _, part := range parts {
				result.WriteString(part.Text)
			}
		}
	}
	return result.String()
}

func antigravityInstruction(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Request struct {
			SystemInstruction struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"systemInstruction"`
		} `json:"request"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode Antigravity payload: %v", err)
	}
	var result strings.Builder
	for _, part := range payload.Request.SystemInstruction.Parts {
		result.WriteString(part.Text)
	}
	return result.String()
}

func claudeInstruction(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		System json.RawMessage `json:"system"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode Claude payload: %v", err)
	}
	var text string
	if json.Unmarshal(payload.System, &text) == nil {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload.System, &parts); err != nil {
		t.Fatalf("decode Claude system blocks: %v", err)
	}
	var result strings.Builder
	for _, part := range parts {
		result.WriteString(part.Text)
	}
	return result.String()
}

func geminiInstruction(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Camel *struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"systemInstruction"`
		Snake *struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"system_instruction"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode Gemini payload: %v", err)
	}
	parts := payload.Camel
	if parts == nil {
		parts = payload.Snake
	}
	if parts == nil {
		t.Fatal("Gemini system instruction missing")
	}
	var result strings.Builder
	for _, part := range parts.Parts {
		result.WriteString(part.Text)
	}
	return result.String()
}

func wrapCaveman(text string) string {
	return wantCavemanStart + "\n" + text + "\n" + wantCavemanEnd
}

func wrapPonytail(text string) string {
	return wantPonytailStart + "\n" + text + "\n" + wantPonytailEnd
}

func openAISystemBody(system string) []byte {
	body, _ := json.Marshal(struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}{Messages: []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{
		{Role: "system", Content: system},
		{Role: "user", Content: "hello"},
	}})
	return body
}
