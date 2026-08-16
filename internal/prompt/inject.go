// Package prompt appends idempotent token-saver instructions to supported
// provider-facing request payloads without re-encoding unrelated JSON fields.
package prompt

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/router-for-me/cpa-plugin-token-saver/internal/protocol"
	"github.com/tidwall/gjson"
)

const (
	cavemanStart  = "[CPA_TOKEN_SAVER_CAVEMAN_START]"
	cavemanEnd    = "[CPA_TOKEN_SAVER_CAVEMAN_END]"
	ponytailStart = "[CPA_TOKEN_SAVER_PONYTAIL_START]"
	ponytailEnd   = "[CPA_TOKEN_SAVER_PONYTAIL_END]"
)

// Options selects prompt stages. An empty level disables that stage.
type Options struct {
	CavemanLevel  string
	PonytailLevel string
}

type promptStage struct {
	start        string
	end          string
	prompt       string
	insertBefore string
	first        bool
}

// Inject appends enabled prompt stages in Caveman then Ponytail order. Unknown
// levels, ineligible translator pairs, and unsupported payloads fail open.
func Inject(body []byte, pair protocol.Pair, options Options) []byte {
	if options.CavemanLevel == "" && options.PonytailLevel == "" {
		return body
	}
	if len(body) == 0 || !pair.Eligible() || !gjson.ValidBytes(body) {
		return body
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() || !supportedShape(body, pair.To) {
		return body
	}

	result := body
	if text, ok := Caveman(options.CavemanLevel); ok {
		result = injectStage(result, pair.To, promptStage{
			start:        cavemanStart,
			end:          cavemanEnd,
			prompt:       text,
			insertBefore: ponytailStart,
			first:        true,
		})
	}
	if text, ok := Ponytail(options.PonytailLevel); ok {
		result = injectStage(result, pair.To, promptStage{
			start:  ponytailStart,
			end:    ponytailEnd,
			prompt: text,
		})
	}
	return result
}

func injectStage(body []byte, target string, stage promptStage) []byte {
	if bytes.Contains(body, []byte(stage.start)) || bytes.Contains(body, []byte(stage.end)) {
		return body
	}

	block := stage.start + "\n" + stage.prompt + "\n" + stage.end
	switch target {
	case "openai":
		return injectOpenAI(body, block, stage)
	case "openai-response", "codex":
		return injectResponses(body, block, stage)
	case "claude":
		return injectClaude(body, block, stage)
	case "gemini":
		return injectGemini(body, block, stage)
	default:
		return body
	}
}

func supportedShape(body []byte, target string) bool {
	switch target {
	case "openai":
		return validOpenAIMessages(gjson.GetBytes(body, "messages"))
	case "openai-response", "codex":
		if !validResponsesInput(gjson.GetBytes(body, "input")) {
			return false
		}
		instructions := gjson.GetBytes(body, "instructions")
		return !instructions.Exists() || instructions.Type == gjson.String
	case "claude":
		if !validClaudeMessages(gjson.GetBytes(body, "messages")) {
			return false
		}
		system := gjson.GetBytes(body, "system")
		return !system.Exists() || system.Type == gjson.String || system.IsArray()
	case "gemini":
		if !validGeminiContents(gjson.GetBytes(body, "contents")) {
			return false
		}
		camel := gjson.GetBytes(body, "systemInstruction")
		snake := gjson.GetBytes(body, "system_instruction")
		if camel.Exists() && snake.Exists() {
			return false
		}
		system := camel
		if !system.Exists() {
			system = snake
		}
		return !system.Exists() || system.IsObject() && gjson.Get(system.Raw, "parts").IsArray()
	default:
		return false
	}
}

func validOpenAIMessages(messages gjson.Result) bool {
	if !messages.IsArray() {
		return false
	}
	items := messages.Array()
	if len(items) == 0 {
		return false
	}
	for _, message := range items {
		if !message.IsObject() {
			return false
		}
		switch message.Get("role").String() {
		case "system", "developer", "user", "assistant", "tool", "function":
		default:
			return false
		}
	}
	return true
}

func validResponsesInput(input gjson.Result) bool {
	if !input.IsArray() {
		return false
	}
	items := input.Array()
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if !item.IsObject() {
			return false
		}
		role := item.Get("role")
		itemType := item.Get("type")
		if role.Exists() {
			switch role.String() {
			case "system", "developer", "user", "assistant":
			default:
				return false
			}
			if !item.Get("content").Exists() {
				return false
			}
			continue
		}
		if itemType.Type != gjson.String || itemType.String() == "" || itemType.String() == "message" {
			return false
		}
	}
	return true
}

func validClaudeMessages(messages gjson.Result) bool {
	if !messages.IsArray() {
		return false
	}
	items := messages.Array()
	if len(items) == 0 {
		return false
	}
	for _, message := range items {
		if !message.IsObject() || !message.Get("content").Exists() {
			return false
		}
		role := message.Get("role").String()
		if role != "user" && role != "assistant" {
			return false
		}
	}
	return true
}

func validGeminiContents(contents gjson.Result) bool {
	if !contents.IsArray() {
		return false
	}
	items := contents.Array()
	if len(items) == 0 {
		return false
	}
	for _, content := range items {
		if !content.IsObject() || !content.Get("parts").IsArray() {
			return false
		}
		role := content.Get("role").String()
		if role != "user" && role != "model" {
			return false
		}
	}
	return true
}

func injectOpenAI(body []byte, block string, stage promptStage) []byte {
	messages := gjson.GetBytes(body, "messages")
	texts, arrays := messageInstructionTargets(body, "messages", "text")
	if selected, ok := selectText(texts, stage.first); ok {
		return injectJSONString(body, selected, block, stage.insertBefore)
	}
	if selected, ok := selectText(arrays, stage.first); ok {
		return appendArrayItem(body, selected, textPart("text", block))
	}
	return prependArrayItem(body, messages, messagePart(block))
}

func injectResponses(body []byte, block string, stage promptStage) []byte {
	texts := make([]gjson.Result, 0, 4)
	instructions := gjson.GetBytes(body, "instructions")
	if instructions.Type == gjson.String {
		texts = append(texts, instructions)
	}
	messageTexts, arrays := messageInstructionTargets(body, "input", "input_text")
	texts = append(texts, messageTexts...)
	if selected, ok := selectText(texts, stage.first); ok {
		return injectJSONString(body, selected, block, stage.insertBefore)
	}
	if selected, ok := selectText(arrays, stage.first); ok {
		return appendArrayItem(body, selected, textPart("input_text", block))
	}
	return appendRootField(body, "instructions", mustJSON(block))
}

func injectClaude(body []byte, block string, stage promptStage) []byte {
	system := gjson.GetBytes(body, "system")
	if system.Type == gjson.String {
		return injectJSONString(body, system, block, stage.insertBefore)
	}
	if system.IsArray() {
		texts := textBlockTargets(system, "text")
		if selected, ok := selectText(texts, stage.first); ok {
			return injectJSONString(body, selected, block, stage.insertBefore)
		}
		return appendArrayItem(body, system, textPart("text", block))
	}
	return appendRootField(body, "system", mustJSON(block))
}

func injectGemini(body []byte, block string, stage promptStage) []byte {
	key := "systemInstruction"
	system := gjson.GetBytes(body, key)
	if !system.Exists() {
		key = "system_instruction"
		system = gjson.GetBytes(body, key)
	}
	if system.Exists() {
		parts := gjson.GetBytes(body, key+".parts")
		texts := textBlockTargets(parts, "")
		if selected, ok := selectText(texts, stage.first); ok {
			return injectJSONString(body, selected, block, stage.insertBefore)
		}
		return appendArrayItem(body, parts, geminiTextPart(block))
	}
	value := []byte(`{"parts":[` + string(geminiTextPart(block)) + `]}`)
	return appendRootField(body, "systemInstruction", value)
}

func messageInstructionTargets(body []byte, arrayPath, textType string) ([]gjson.Result, []gjson.Result) {
	messages := gjson.GetBytes(body, arrayPath)
	var texts []gjson.Result
	var arrays []gjson.Result
	for _, message := range messages.Array() {
		if !message.IsObject() {
			continue
		}
		role := message.Get("role")
		if role.String() != "system" && role.String() != "developer" {
			continue
		}
		content := message.Get("content")
		if content.Type == gjson.String {
			texts = append(texts, content)
			continue
		}
		if content.IsArray() {
			arrays = append(arrays, content)
			texts = append(texts, textBlockTargets(content, textType)...)
		}
	}
	return texts, arrays
}

func textBlockTargets(array gjson.Result, requiredType string) []gjson.Result {
	var texts []gjson.Result
	for _, part := range array.Array() {
		if !part.IsObject() {
			continue
		}
		if requiredType != "" {
			kind := part.Get("type")
			if kind.Type != gjson.String || kind.String() != requiredType {
				continue
			}
		}
		text := part.Get("text")
		if text.Type == gjson.String {
			texts = append(texts, text)
		}
	}
	return texts
}

func selectText(results []gjson.Result, first bool) (gjson.Result, bool) {
	if len(results) == 0 {
		return gjson.Result{}, false
	}
	if first {
		return results[0], true
	}
	return results[len(results)-1], true
}

func injectJSONString(body []byte, target gjson.Result, block, beforeMarker string) []byte {
	if target.Type != gjson.String || len(target.Raw) < 2 {
		return body
	}
	position := len(target.Raw) - 1
	insertion := block
	if beforeMarker != "" {
		if marker := strings.Index(target.Raw, beforeMarker); marker >= 0 {
			position = marker
			insertion += "\n\n"
		} else if target.String() != "" {
			insertion = "\n\n" + insertion
		}
	} else if target.String() != "" {
		insertion = "\n\n" + insertion
	}
	encoded := mustJSON(insertion)
	return insertBytes(body, target.Index+position, encoded[1:len(encoded)-1])
}

func prependArrayItem(body []byte, array gjson.Result, item []byte) []byte {
	if !array.IsArray() || len(array.Raw) < 2 {
		return body
	}
	insertion := item
	if len(array.Array()) > 0 {
		insertion = append(append([]byte(nil), item...), ',')
	}
	return insertBytes(body, array.Index+1, insertion)
}

func appendArrayItem(body []byte, array gjson.Result, item []byte) []byte {
	if !array.IsArray() || len(array.Raw) < 2 {
		return body
	}
	insertion := item
	if len(array.Array()) > 0 {
		insertion = append([]byte{','}, item...)
	}
	return insertBytes(body, array.Index+len(array.Raw)-1, insertion)
}

func appendRootField(body []byte, key string, value []byte) []byte {
	root := gjson.ParseBytes(body)
	raw := strings.TrimRight(root.Raw, " \t\r\n")
	if !root.IsObject() || len(raw) < 2 {
		return body
	}
	field := append(mustJSON(key), ':')
	field = append(field, value...)
	field = append([]byte{','}, field...)
	return insertBytes(body, root.Index+len(raw)-1, field)
}

func insertBytes(body []byte, position int, insertion []byte) []byte {
	if position < 0 || position > len(body) || len(insertion) == 0 {
		return body
	}
	out := make([]byte, 0, len(body)+len(insertion))
	out = append(out, body[:position]...)
	out = append(out, insertion...)
	out = append(out, body[position:]...)
	return out
}

func messagePart(text string) []byte {
	return []byte(`{"role":"system","content":` + string(mustJSON(text)) + `}`)
}

func textPart(kind, text string) []byte {
	return []byte(`{"type":` + string(mustJSON(kind)) + `,"text":` + string(mustJSON(text)) + `}`)
}

func geminiTextPart(text string) []byte {
	return []byte(`{"text":` + string(mustJSON(text)) + `}`)
}

func mustJSON(value string) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}
