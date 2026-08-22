// Package protocol exposes field-preserving views over provider request payloads.
package protocol

import (
	"bytes"
	"encoding/json"
	"sort"

	"github.com/tidwall/gjson"
)

// Pair identifies the host's native request translator route. Values are the
// exact CLIProxyAPI format strings, not provider aliases or plugin formats.
type Pair struct {
	From string
	To   string
}

var nativeMessagePairs = map[Pair]struct{}{
	{From: "openai", To: "openai"}:                {},
	{From: "claude", To: "openai"}:                {},
	{From: "gemini", To: "openai"}:                {},
	{From: "interactions", To: "openai"}:          {},
	{From: "openai-response", To: "openai"}:       {},
	{From: "interactions", To: "openai-response"}: {},
	{From: "openai", To: "claude"}:                {},
	{From: "openai-response", To: "claude"}:       {},
	{From: "gemini", To: "claude"}:                {},
	{From: "interactions", To: "claude"}:          {},
	{From: "openai", To: "codex"}:                 {},
	{From: "openai-response", To: "codex"}:        {},
	{From: "claude", To: "codex"}:                 {},
	{From: "gemini", To: "codex"}:                 {},
	{From: "interactions", To: "codex"}:           {},
	{From: "openai", To: "gemini"}:                {},
	{From: "openai-response", To: "gemini"}:       {},
	{From: "claude", To: "gemini"}:                {},
	{From: "gemini", To: "gemini"}:                {},
	{From: "interactions", To: "gemini"}:          {},
	{From: "openai-response", To: "antigravity"}:  {},
}

// Eligible reports whether CLIProxyAPI has a verified built-in request
// translator for the pair and the target is one of the supported message
// protocols. Plugin-only and unknown routes deliberately fail closed.
func (p Pair) Eligible() bool {
	_, ok := nativeMessagePairs[p]
	return ok
}

// SlotKind identifies a recognized textual tool-result field.
type SlotKind string

const (
	SlotOpenAIChat      SlotKind = "openai-chat-tool"
	SlotOpenAIResponses SlotKind = "openai-responses-output"
	SlotClaudeMessages  SlotKind = "claude-tool-result"
	SlotGemini          SlotKind = "gemini-function-response"
)

// TextSlot is a read-only description of one replaceable text field.
type TextSlot struct {
	Text     string
	ResultID string
	Kind     SlotKind
	Error    bool
	start    int
	end      int
}

// Payload retains the original JSON bytes and byte spans for recognized text
// fields. Rewriting a slot never re-encodes any surrounding object or block.
type Payload struct {
	raw   []byte
	slots []TextSlot
}

// View validates body, verifies pair eligibility, and returns a provider-facing
// message view. Non-message roots, malformed JSON, and unknown pairs bypass.
func View(body []byte, pair Pair) (Payload, bool) {
	if len(body) == 0 || !pair.Eligible() || !gjson.ValidBytes(body) {
		return Payload{}, false
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return Payload{}, false
	}

	view := Payload{raw: body}
	var recognized bool
	switch pair.To {
	case "openai":
		recognized = view.openAIChat()
	case "openai-response", "codex":
		recognized = view.openAIResponses()
	case "claude":
		recognized = view.claudeMessages()
	case "gemini":
		recognized = view.gemini()
	}
	if !recognized {
		return Payload{}, false
	}
	return view, true
}

// Slots returns the recognized textual fields in payload order.
func (p Payload) Slots() []TextSlot {
	return append([]TextSlot(nil), p.slots...)
}

// Rewrite replaces selected slots by index. All untouched bytes, including
// opaque blocks, number literals, key order, and whitespace, remain unchanged.
func (p Payload) Rewrite(replacements map[int]string) []byte {
	if len(replacements) == 0 || len(p.raw) == 0 {
		return p.raw
	}
	type edit struct {
		start int
		end   int
		raw   []byte
	}
	edits := make([]edit, 0, len(replacements))
	for index, replacement := range replacements {
		if index < 0 || index >= len(p.slots) {
			continue
		}
		slot := p.slots[index]
		encoded, err := json.Marshal(replacement)
		if err != nil {
			continue
		}
		edits = append(edits, edit{start: slot.start, end: slot.end, raw: encoded})
	}
	if len(edits) == 0 {
		return p.raw
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	valid := edits[:0]
	outputLength := len(p.raw)
	previousEnd := 0
	for _, current := range edits {
		if current.start < previousEnd || current.end < current.start || current.end > len(p.raw) {
			continue
		}
		valid = append(valid, current)
		outputLength += len(current.raw) - (current.end - current.start)
		previousEnd = current.end
	}
	if len(valid) == 0 {
		return p.raw
	}
	out := make([]byte, 0, outputLength)
	previousEnd = 0
	for _, current := range valid {
		out = append(out, p.raw[previousEnd:current.start]...)
		out = append(out, current.raw...)
		previousEnd = current.end
	}
	out = append(out, p.raw[previousEnd:]...)
	return out
}

func (p *Payload) openAIChat() bool {
	messages := gjson.GetBytes(p.raw, "messages")
	if !messages.IsArray() {
		return false
	}
	for i, message := range messages.Array() {
		base := "messages." + indexString(i)
		if !message.IsObject() || stringAt(p.raw, base+".role") != "tool" {
			continue
		}
		resultID := stringAt(p.raw, base+".tool_call_id")
		if resultID == "" {
			continue
		}
		errorResult := isErrorObject(p.raw, base)
		contentPath := base + ".content"
		content := gjson.GetBytes(p.raw, contentPath)
		if content.Type == gjson.String {
			p.add(content, resultID, SlotOpenAIChat, errorResult)
			continue
		}
		if !content.IsArray() {
			continue
		}
		for j, block := range content.Array() {
			blockPath := contentPath + "." + indexString(j)
			if block.IsObject() && stringAt(p.raw, blockPath+".type") == "text" {
				p.add(gjson.GetBytes(p.raw, blockPath+".text"), resultID, SlotOpenAIChat, errorResult)
			}
		}
	}
	return true
}

func (p *Payload) openAIResponses() bool {
	input := gjson.GetBytes(p.raw, "input")
	if !input.IsArray() {
		return false
	}
	for i, item := range input.Array() {
		base := "input." + indexString(i)
		if !item.IsObject() || stringAt(p.raw, base+".type") != "function_call_output" {
			continue
		}
		resultID := stringAt(p.raw, base+".call_id")
		if resultID == "" {
			continue
		}
		errorResult := isErrorObject(p.raw, base)
		outputPath := base + ".output"
		output := gjson.GetBytes(p.raw, outputPath)
		if output.Type == gjson.String {
			p.add(output, resultID, SlotOpenAIResponses, errorResult)
			continue
		}
		if !output.IsArray() {
			continue
		}
		for j, block := range output.Array() {
			blockPath := outputPath + "." + indexString(j)
			if block.IsObject() && stringAt(p.raw, blockPath+".type") == "input_text" {
				p.add(gjson.GetBytes(p.raw, blockPath+".text"), resultID, SlotOpenAIResponses, errorResult)
			}
		}
	}
	return true
}

func (p *Payload) claudeMessages() bool {
	messages := gjson.GetBytes(p.raw, "messages")
	if !messages.IsArray() {
		return false
	}
	for i, message := range messages.Array() {
		contentPath := "messages." + indexString(i) + ".content"
		content := gjson.GetBytes(p.raw, contentPath)
		if !message.IsObject() || !content.IsArray() {
			continue
		}
		for j, block := range content.Array() {
			base := contentPath + "." + indexString(j)
			if !block.IsObject() || stringAt(p.raw, base+".type") != "tool_result" {
				continue
			}
			resultID := stringAt(p.raw, base+".tool_use_id")
			if resultID == "" {
				continue
			}
			errorResult := isErrorObject(p.raw, base)
			resultPath := base + ".content"
			result := gjson.GetBytes(p.raw, resultPath)
			if result.Type == gjson.String {
				p.add(result, resultID, SlotClaudeMessages, errorResult)
				continue
			}
			if !result.IsArray() {
				continue
			}
			for k, part := range result.Array() {
				partPath := resultPath + "." + indexString(k)
				if part.IsObject() && stringAt(p.raw, partPath+".type") == "text" {
					p.add(gjson.GetBytes(p.raw, partPath+".text"), resultID, SlotClaudeMessages, errorResult)
				}
			}
		}
	}
	return true
}

func (p *Payload) gemini() bool {
	contents := gjson.GetBytes(p.raw, "contents")
	if !contents.IsArray() {
		return false
	}
	for i, content := range contents.Array() {
		partsPath := "contents." + indexString(i) + ".parts"
		parts := gjson.GetBytes(p.raw, partsPath)
		if !content.IsObject() || !parts.IsArray() {
			continue
		}
		for j, part := range parts.Array() {
			base := partsPath + "." + indexString(j) + ".functionResponse"
			response := gjson.GetBytes(p.raw, base)
			if !part.IsObject() || !response.IsObject() {
				continue
			}
			resultID := stringAt(p.raw, base+".id")
			if resultID == "" {
				resultID = stringAt(p.raw, base+".name")
			}
			if resultID == "" {
				continue
			}
			errorResult := isErrorObject(p.raw, base) || gjson.GetBytes(p.raw, base+".response.error").Exists()
			p.add(gjson.GetBytes(p.raw, base+".response.result"), resultID, SlotGemini, errorResult)
		}
	}
	return true
}

func (p *Payload) add(result gjson.Result, resultID string, kind SlotKind, errorResult bool) {
	if result.Type != gjson.String || result.Index < 0 {
		return
	}
	start := result.Index
	end := start + len(result.Raw)
	if start < 0 || end > len(p.raw) || !bytes.Equal(p.raw[start:end], []byte(result.Raw)) {
		return
	}
	p.slots = append(p.slots, TextSlot{
		Text:     result.String(),
		ResultID: resultID,
		Kind:     kind,
		Error:    errorResult,
		start:    start,
		end:      end,
	})
}

func stringAt(raw []byte, path string) string {
	result := gjson.GetBytes(raw, path)
	if result.Type != gjson.String {
		return ""
	}
	return result.String()
}

func isErrorObject(raw []byte, base string) bool {
	if gjson.GetBytes(raw, base+".is_error").Bool() {
		return true
	}
	return stringAt(raw, base+".status") == "error"
}

func indexString(index int) string {
	if index < 10 {
		return string(rune('0' + index))
	}
	// Arrays in supported payloads may be large; avoid importing formatting on
	// every traversal while retaining exact decimal gjson paths.
	var digits [20]byte
	position := len(digits)
	for index > 0 {
		position--
		digits[position] = byte('0' + index%10)
		index /= 10
	}
	return string(digits[position:])
}
