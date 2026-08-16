package rtk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/cpa-plugin-token-saver/internal/protocol"
	"github.com/router-for-me/cpa-plugin-token-saver/internal/rtk/filters"
)

func TestApplyCompressesFourProviderPayloadsWithoutChangingResultIdentity(t *testing.T) {
	diff := longDiff(180)
	quoted, _ := json.Marshal(diff)
	tests := []struct {
		name string
		pair protocol.Pair
		body string
		id   string
	}{
		{name: "openai chat", pair: protocol.Pair{From: "openai", To: "openai"}, body: fmt.Sprintf(`{"messages":[{"role":"assistant","content":"leave me"},{"role":"tool","tool_call_id":"call_1","content":%s}]}`, quoted), id: "call_1"},
		{name: "responses codex", pair: protocol.Pair{From: "openai-response", To: "codex"}, body: fmt.Sprintf(`{"input":[{"type":"function_call_output","call_id":"call_2","output":%s}]}`, quoted), id: "call_2"},
		{name: "claude", pair: protocol.Pair{From: "openai", To: "claude"}, body: fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_3","content":%s}]}]}`, quoted), id: "toolu_3"},
		{name: "gemini", pair: protocol.Pair{From: "openai", To: "gemini"}, body: fmt.Sprintf(`{"contents":[{"role":"user","parts":[{"functionResponse":{"id":"call_4","name":"run","response":{"result":%s}}}]}]}`, quoted), id: "call_4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Apply([]byte(tt.body), tt.pair)
			if len(got) >= len(tt.body) {
				t.Fatalf("Apply() did not shrink payload: before=%d after=%d", len(tt.body), len(got))
			}
			if !bytes.Contains(got, []byte(tt.id)) {
				t.Fatalf("Apply() lost result id %q: %s", tt.id, got)
			}
			view, ok := protocol.View(got, tt.pair)
			if !ok || len(view.Slots()) != 1 || !strings.Contains(view.Slots()[0].Text, "lines truncated") {
				t.Fatalf("compressed slots = %#v, recognized=%v", view.Slots(), ok)
			}
		})
	}
}

func TestApplyPreservesParallelOrderOpaqueBlocksAndErrors(t *testing.T) {
	diff := longDiff(160)
	quoted, _ := json.Marshal(diff)
	body := []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"one","content":[{"type":"text","text":%s},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AQID"}}]},{"type":"tool_result","tool_use_id":"err","is_error":true,"content":%s},{"type":"tool_result","tool_use_id":"two","content":%s}]}]}`, quoted, quoted, quoted))
	pair := protocol.Pair{From: "openai", To: "claude"}
	got := Apply(body, pair)
	view, ok := protocol.View(got, pair)
	if !ok || len(view.Slots()) != 3 {
		t.Fatalf("slots = %#v, recognized=%v", view.Slots(), ok)
	}
	if ids := []string{view.Slots()[0].ResultID, view.Slots()[1].ResultID, view.Slots()[2].ResultID}; fmt.Sprint(ids) != "[one err two]" {
		t.Fatalf("result order = %v", ids)
	}
	if view.Slots()[0].Text == diff || view.Slots()[2].Text == diff || view.Slots()[1].Text != diff {
		t.Fatal("successful parallel outputs must compress while error output remains unchanged")
	}
	if !bytes.Contains(got, []byte(`"data":"AQID"`)) {
		t.Fatalf("opaque image block changed: %s", got)
	}
}

func TestApplyBypassesUnsafeInputsByteIdentically(t *testing.T) {
	diff := longDiff(160)
	quoted, _ := json.Marshal(diff)
	valid := []byte(fmt.Sprintf(` { "messages" : [ { "role":"tool", "tool_call_id":"x", "content":%s } ] } `, quoted))
	tests := []struct {
		name string
		body []byte
		pair protocol.Pair
	}{
		{name: "unknown pair", body: valid, pair: protocol.Pair{From: "plugin-format", To: "openai"}},
		{name: "malformed", body: []byte(`{"messages":[`), pair: protocol.Pair{From: "openai", To: "openai"}},
		{name: "empty", body: nil, pair: protocol.Pair{From: "openai", To: "openai"}},
		{name: "non-message endpoint", body: []byte(fmt.Sprintf(`{"model":"gpt-image-1","prompt":%s}`, quoted)), pair: protocol.Pair{From: "openai", To: "openai"}},
		{name: "under 500 bytes", body: openAIToolBody(grepSized(499)), pair: protocol.Pair{From: "openai", To: "openai"}},
		{name: "over 10 MiB", body: openAIToolBody(strings.Repeat("same line\n", MaxRawBytes/10+2)), pair: protocol.Pair{From: "openai", To: "openai"}},
		{name: "error result", body: []byte(fmt.Sprintf(`{"messages":[{"role":"tool","tool_call_id":"e","is_error":true,"content":%s}]}`, quoted)), pair: protocol.Pair{From: "openai", To: "openai"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Apply(tt.body, tt.pair); !bytes.Equal(got, tt.body) {
				t.Fatalf("bypass changed bytes: before=%d after=%d", len(tt.body), len(got))
			}
		})
	}
}

func TestApplyHonorsExactSizeBoundariesAndWindowsPaths(t *testing.T) {
	for _, size := range []int{MinCompressBytes, MaxRawBytes} {
		text := grepSized(size)
		body := openAIToolBody(text)
		got := Apply(body, protocol.Pair{From: "openai", To: "openai"})
		if bytes.Equal(got, body) {
			t.Fatalf("exact boundary %d bytes should be eligible", size)
		}
	}

	var paths []string
	for i := 0; i < 80; i++ {
		paths = append(paths, fmt.Sprintf(`C:\Users\me\project\src\folder\file-%03d.go`, i))
	}
	pair := protocol.Pair{From: "openai", To: "openai"}
	got := Apply(openAIToolBody(strings.Join(paths, "\n")), pair)
	view, ok := protocol.View(got, pair)
	if !ok || len(view.Slots()) != 1 || strings.Contains(view.Slots()[0].Text, `\`) || !strings.Contains(view.Slots()[0].Text, "C:/Users/me/project/src/folder/") {
		t.Fatalf("Windows find output was not grouped safely: %#v", view.Slots())
	}
}

func TestFilterRegistryAndDetectionCoverCurrentRTKSet(t *testing.T) {
	want := []string{"git-diff", "git-status", "git-log", "grep", "find", "dedup-log", "ls", "tree", "smart-truncate", "read-numbered", "search-list", "build-output"}
	if got := filters.Names(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("filters.Names() = %v, want %v", got, want)
	}

	tests := []struct{ name, text, want string }{
		{name: "git log", text: "commit abc1234def5678abc1234def5678abc1234def5\nAuthor: Dev\n\n    subject", want: "git-log"},
		{name: "git diff", text: "diff --git a/x b/x\n@@ -1 +1 @@\n-a\n+b", want: "git-diff"},
		{name: "git status", text: "On branch main\nChanges not staged for commit:\n modified: x", want: "git-status"},
		{name: "build", text: "   Compiling one v1\n   Compiling two v1\n    Finished dev", want: "build-output"},
		{name: "grep", text: "a.go:1:x\nb.go:2:y\nc.go:3:z", want: "grep"},
		{name: "tree", text: ".\n├── src\n│   └── main.go", want: "tree"},
		{name: "ls", text: "total 3\n-rw-r--r-- 1 u g 1 Jan 1 12:00 a\n-rw-r--r-- 1 u g 1 Jan 1 12:00 b\n-rw-r--r-- 1 u g 1 Jan 1 12:00 c", want: "ls"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := Detect(tt.text)
			if filter.Name != tt.want {
				t.Fatalf("Detect() = %q, want %q", filter.Name, tt.want)
			}
		})
	}
}

func TestAcceptReplacementRejectsEmptyUnchangedAndGrowth(t *testing.T) {
	for _, candidate := range []string{"", "original", "original-but-longer"} {
		if got, ok := acceptReplacement("original", candidate); ok || got != "original" {
			t.Fatalf("acceptReplacement(%q) = %q, %v", candidate, got, ok)
		}
	}
}

func longDiff(lines int) string {
	values := []string{"diff --git a/foo.go b/foo.go", "--- a/foo.go", "+++ b/foo.go", "@@ -1 +1 @@"}
	for i := 0; i < lines; i++ {
		values = append(values, fmt.Sprintf("+line %03d %s", i, strings.Repeat("x", 30)))
	}
	return strings.Join(values, "\n")
}

func openAIToolBody(text string) []byte {
	quoted, _ := json.Marshal(text)
	return []byte(fmt.Sprintf(`{"messages":[{"role":"tool","tool_call_id":"x","content":%s}]}`, quoted))
}

func grepSized(size int) string {
	var b strings.Builder
	for i := 1; b.Len() < size; i++ {
		line := fmt.Sprintf("src/file.go:%d:%s", i, strings.Repeat("x", 24))
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		remaining := size - b.Len()
		if len(line) > remaining {
			line = line[:remaining]
		}
		b.WriteString(line)
	}
	return b.String()
}
