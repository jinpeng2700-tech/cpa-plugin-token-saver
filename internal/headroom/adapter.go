package headroom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"

	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/protocol"
	"github.com/tidwall/gjson"
)

const maskedText = "__CPA_HEADROOM_MUTABLE_TEXT__"

// Adapter projects supported provider payloads into Headroom's OpenAI message
// contract and copies back only text fields declared by that projection.
type Adapter struct {
	client *Client
}

// NewAdapter binds the protocol adapter to one configuration-scoped client.
func NewAdapter(client *Client) *Adapter {
	return &Adapter{client: client}
}

// Apply compresses one provider-facing payload. Every bypass or failure returns
// the exact input slice; only a fully validated response can produce new bytes.
func (adapter *Adapter) Apply(ctx context.Context, body []byte, pair protocol.Pair, model string) ([]byte, Outcome) {
	if len(body) == 0 || !json.Valid(body) {
		return body, OutcomeUnsupportedStructure
	}
	if len(body) > maxPayloadBytes {
		return body, OutcomeRequestTooLarge
	}
	if adapter == nil || adapter.client == nil || !pair.Eligible() {
		return body, OutcomeUnsupportedFormat
	}

	var (
		view *projection
		err  error
	)
	switch pair.To {
	case "openai":
		view, err = projectOpenAIChat(body)
	case "openai-response", "codex":
		view, err = projectResponses(body)
	case "claude":
		view, err = projectClaude(body)
	default:
		return body, OutcomeUnsupportedFormat
	}
	if err != nil || view == nil || len(view.targets) == 0 {
		return body, OutcomeUnsupportedStructure
	}

	wire, errMarshal := json.Marshal(struct {
		Messages []any  `json:"messages"`
		Model    string `json:"model"`
	}{Messages: view.messages, Model: model})
	if errMarshal != nil {
		return body, OutcomeUnsupportedStructure
	}

	var replacements []string
	outcome := adapter.client.Compress(ctx, wire, func(messages []json.RawMessage) error {
		candidate, errValidate := view.validate(messages)
		if errValidate != nil {
			return errValidate
		}
		replacements = candidate
		return nil
	})
	if outcome != OutcomeApplied {
		return body, outcome
	}
	output, changed := view.rewrite(body, replacements)
	if !changed {
		return body, OutcomeNoChange
	}
	if len(output) > maxPayloadBytes {
		return body, OutcomeResponseTooLarge
	}
	return output, OutcomeApplied
}

type textTarget struct {
	start        int
	end          int
	messageIndex int
	blockIndex   int
	original     string
}

type projection struct {
	messages []any
	targets  []textTarget
}

func projectOpenAIChat(body []byte) (*projection, error) {
	root, errRoot := decodeObject(body)
	if errRoot != nil {
		return nil, errRoot
	}
	messages, ok := root["messages"].([]any)
	if !ok || len(messages) == 0 {
		return nil, fmt.Errorf("missing messages")
	}
	view := &projection{messages: messages}
	for messageIndex, rawMessage := range messages {
		message, okMessage := rawMessage.(map[string]any)
		if !okMessage {
			return nil, fmt.Errorf("message is not an object")
		}
		role, okRole := message["role"].(string)
		if !okRole || !validOpenAIRole(role) {
			return nil, fmt.Errorf("unsupported role")
		}
		if errTools := validateOpenAIToolFields(message, role); errTools != nil {
			return nil, errTools
		}
		content, hasContent := message["content"]
		if !hasContent {
			if role == "assistant" && message["tool_calls"] != nil {
				continue
			}
			return nil, fmt.Errorf("missing content")
		}
		mutable := role != "system" && role != "developer"
		switch typed := content.(type) {
		case string:
			if mutable {
				if errTarget := view.addTarget(body, fmt.Sprintf("messages.%d.content", messageIndex), messageIndex, -1, typed); errTarget != nil {
					return nil, errTarget
				}
			}
		case []any:
			for blockIndex, rawBlock := range typed {
				block, okBlock := rawBlock.(map[string]any)
				if !okBlock || block["type"] != "text" {
					return nil, fmt.Errorf("unsupported content block")
				}
				text, okText := block["text"].(string)
				if !okText {
					return nil, fmt.Errorf("text block is missing text")
				}
				if mutable {
					path := fmt.Sprintf("messages.%d.content.%d.text", messageIndex, blockIndex)
					if errTarget := view.addTarget(body, path, messageIndex, blockIndex, text); errTarget != nil {
						return nil, errTarget
					}
				}
			}
		case nil:
			if role != "assistant" || message["tool_calls"] == nil {
				return nil, fmt.Errorf("unsupported null content")
			}
		default:
			return nil, fmt.Errorf("unsupported content")
		}
	}
	return view, nil
}

func projectResponses(body []byte) (*projection, error) {
	root, errRoot := decodeObject(body)
	if errRoot != nil {
		return nil, errRoot
	}
	input, ok := root["input"].([]any)
	if !ok || len(input) == 0 {
		return nil, fmt.Errorf("missing input")
	}
	for _, rawItem := range input {
		item, okItem := rawItem.(map[string]any)
		if okItem && responsesOutputType(item["type"]) {
			return projectResponsesOutputs(body, input)
		}
	}
	view := &projection{}
	for itemIndex, rawItem := range input {
		item, okItem := rawItem.(map[string]any)
		if !okItem || item["type"] != "message" {
			return nil, fmt.Errorf("Responses input is not message-only")
		}
		role, okRole := item["role"].(string)
		if !okRole || !validOpenAIRole(role) || role == "tool" {
			return nil, fmt.Errorf("unsupported Responses role")
		}
		content, hasContent := item["content"]
		if !hasContent {
			return nil, fmt.Errorf("missing Responses content")
		}
		messageIndex := len(view.messages)
		projected := map[string]any{"role": role}
		mutable := role != "system" && role != "developer"
		switch typed := content.(type) {
		case string:
			projected["content"] = typed
			view.messages = append(view.messages, projected)
			if mutable {
				path := fmt.Sprintf("input.%d.content", itemIndex)
				if errTarget := view.addTarget(body, path, messageIndex, -1, typed); errTarget != nil {
					return nil, errTarget
				}
			}
		case []any:
			if len(typed) == 1 {
				block, okBlock := typed[0].(map[string]any)
				if !okBlock {
					return nil, fmt.Errorf("Responses block is not an object")
				}
				blockType, _ := block["type"].(string)
				if blockType != "input_text" && blockType != "output_text" {
					return nil, fmt.Errorf("unsupported Responses block")
				}
				text, okText := block["text"].(string)
				if !okText {
					return nil, fmt.Errorf("Responses text block is missing text")
				}
				projected["content"] = text
				view.messages = append(view.messages, projected)
				if mutable {
					path := fmt.Sprintf("input.%d.content.0.text", itemIndex)
					if errTarget := view.addTarget(body, path, messageIndex, -1, text); errTarget != nil {
						return nil, errTarget
					}
				}
				continue
			}
			blocks := make([]any, 0, len(typed))
			for blockIndex, rawBlock := range typed {
				block, okBlock := rawBlock.(map[string]any)
				if !okBlock {
					return nil, fmt.Errorf("Responses block is not an object")
				}
				blockType, _ := block["type"].(string)
				if blockType != "input_text" && blockType != "output_text" {
					return nil, fmt.Errorf("unsupported Responses block")
				}
				text, okText := block["text"].(string)
				if !okText {
					return nil, fmt.Errorf("Responses text block is missing text")
				}
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
				if mutable {
					path := fmt.Sprintf("input.%d.content.%d.text", itemIndex, blockIndex)
					if errTarget := view.addTarget(body, path, messageIndex, blockIndex, text); errTarget != nil {
						return nil, errTarget
					}
				}
			}
			projected["content"] = blocks
			view.messages = append(view.messages, projected)
		default:
			return nil, fmt.Errorf("unsupported Responses content")
		}
	}
	return view, nil
}

func projectResponsesOutputs(body []byte, input []any) (*projection, error) {
	view := &projection{}
	for itemIndex, rawItem := range input {
		item, okItem := rawItem.(map[string]any)
		if !okItem || !responsesOutputType(item["type"]) {
			continue
		}
		callID, okCallID := item["call_id"].(string)
		if !okCallID || callID == "" {
			continue
		}
		add := func(path, text string) error {
			messageIndex := len(view.messages)
			view.messages = append(view.messages, map[string]any{
				"role": "tool", "tool_call_id": callID, "content": text,
			})
			return view.addTarget(body, path, messageIndex, -1, text)
		}
		output, exists := item["output"]
		if !exists {
			continue
		}
		switch typed := output.(type) {
		case string:
			if errTarget := add(fmt.Sprintf("input.%d.output", itemIndex), typed); errTarget != nil {
				return nil, errTarget
			}
		case []any:
			for blockIndex, rawBlock := range typed {
				block, okBlock := rawBlock.(map[string]any)
				if !okBlock || (block["type"] != "input_text" && block["type"] != "output_text") {
					continue
				}
				text, okText := block["text"].(string)
				if !okText {
					continue
				}
				path := fmt.Sprintf("input.%d.output.%d.text", itemIndex, blockIndex)
				if errTarget := add(path, text); errTarget != nil {
					return nil, errTarget
				}
			}
		}
	}
	return view, nil
}

func responsesOutputType(value any) bool {
	switch value {
	case "custom_tool_call_output", "function_call_output", "local_shell_call_output", "apply_patch_call_output":
		return true
	default:
		return false
	}
}

func projectClaude(body []byte) (*projection, error) {
	root, errRoot := decodeObject(body)
	if errRoot != nil {
		return nil, errRoot
	}
	view := &projection{}
	if system, exists := root["system"]; exists {
		switch typed := system.(type) {
		case string:
			view.messages = append(view.messages, map[string]any{"role": "system", "content": typed})
		case []any:
			for _, rawBlock := range typed {
				block, okBlock := rawBlock.(map[string]any)
				if !okBlock || block["type"] != "text" {
					return nil, fmt.Errorf("unsupported Claude system block")
				}
				text, okText := block["text"].(string)
				if !okText {
					return nil, fmt.Errorf("Claude system text is missing")
				}
				view.messages = append(view.messages, map[string]any{"role": "system", "content": text})
			}
		default:
			return nil, fmt.Errorf("unsupported Claude system")
		}
	}

	messages, okMessages := root["messages"].([]any)
	if !okMessages || len(messages) == 0 {
		return nil, fmt.Errorf("missing Claude messages")
	}
	for messageIndex, rawMessage := range messages {
		message, okMessage := rawMessage.(map[string]any)
		if !okMessage {
			return nil, fmt.Errorf("Claude message is not an object")
		}
		role, okRole := message["role"].(string)
		if !okRole || (role != "user" && role != "assistant") {
			return nil, fmt.Errorf("unsupported Claude role")
		}
		content, hasContent := message["content"]
		if !hasContent {
			return nil, fmt.Errorf("missing Claude content")
		}
		switch typed := content.(type) {
		case string:
			projectedIndex := len(view.messages)
			view.messages = append(view.messages, map[string]any{"role": role, "content": typed})
			path := fmt.Sprintf("messages.%d.content", messageIndex)
			if errTarget := view.addTarget(body, path, projectedIndex, -1, typed); errTarget != nil {
				return nil, errTarget
			}
		case []any:
			for blockIndex, rawBlock := range typed {
				block, okBlock := rawBlock.(map[string]any)
				if !okBlock {
					return nil, fmt.Errorf("Claude block is not an object")
				}
				switch block["type"] {
				case "text":
					text, okText := block["text"].(string)
					if !okText {
						return nil, fmt.Errorf("Claude text block is missing text")
					}
					projectedIndex := len(view.messages)
					view.messages = append(view.messages, map[string]any{"role": role, "content": text})
					path := fmt.Sprintf("messages.%d.content.%d.text", messageIndex, blockIndex)
					if errTarget := view.addTarget(body, path, projectedIndex, -1, text); errTarget != nil {
						return nil, errTarget
					}
				case "tool_use":
					if role != "assistant" {
						return nil, fmt.Errorf("Claude tool_use has wrong role")
					}
					call, errCall := projectClaudeToolUse(block)
					if errCall != nil {
						return nil, errCall
					}
					view.messages = append(view.messages, map[string]any{"role": "assistant", "content": "", "tool_calls": []any{call}})
				case "tool_result":
					if role != "user" {
						return nil, fmt.Errorf("Claude tool_result has wrong role")
					}
					if errResult := view.projectClaudeToolResult(body, messageIndex, blockIndex, block); errResult != nil {
						return nil, errResult
					}
				default:
					return nil, fmt.Errorf("unsupported Claude block")
				}
			}
		default:
			return nil, fmt.Errorf("unsupported Claude content")
		}
	}
	return view, nil
}

func projectClaudeToolUse(block map[string]any) (map[string]any, error) {
	id, okID := block["id"].(string)
	name, okName := block["name"].(string)
	input, hasInput := block["input"]
	if !okID || id == "" || !okName || name == "" || !hasInput {
		return nil, fmt.Errorf("invalid Claude tool_use")
	}
	arguments, errArguments := json.Marshal(input)
	if errArguments != nil {
		return nil, fmt.Errorf("marshal Claude tool input: %w", errArguments)
	}
	return map[string]any{
		"id":   id,
		"type": "function",
		"function": map[string]any{
			"name":      name,
			"arguments": string(arguments),
		},
	}, nil
}

func (view *projection) projectClaudeToolResult(body []byte, messageIndex, blockIndex int, block map[string]any) error {
	toolUseID, okID := block["tool_use_id"].(string)
	if !okID || toolUseID == "" {
		return fmt.Errorf("invalid Claude tool_result")
	}
	content, hasContent := block["content"]
	if !hasContent {
		return fmt.Errorf("Claude tool_result is missing content")
	}
	projectedIndex := len(view.messages)
	projected := map[string]any{"role": "tool", "tool_call_id": toolUseID}
	switch typed := content.(type) {
	case string:
		projected["content"] = typed
		view.messages = append(view.messages, projected)
		path := fmt.Sprintf("messages.%d.content.%d.content", messageIndex, blockIndex)
		return view.addTarget(body, path, projectedIndex, -1, typed)
	case []any:
		blocks := make([]any, 0, len(typed))
		for nestedIndex, rawNested := range typed {
			nested, okNested := rawNested.(map[string]any)
			if !okNested || nested["type"] != "text" {
				return fmt.Errorf("unsupported Claude tool_result block")
			}
			text, okText := nested["text"].(string)
			if !okText {
				return fmt.Errorf("Claude tool_result text is missing")
			}
			blocks = append(blocks, map[string]any{"type": "text", "text": text})
			path := fmt.Sprintf("messages.%d.content.%d.content.%d.text", messageIndex, blockIndex, nestedIndex)
			if errTarget := view.addTarget(body, path, projectedIndex, nestedIndex, text); errTarget != nil {
				return errTarget
			}
		}
		projected["content"] = blocks
		view.messages = append(view.messages, projected)
		return nil
	default:
		return fmt.Errorf("unsupported Claude tool_result content")
	}
}

func validateOpenAIToolFields(message map[string]any, role string) error {
	if role == "tool" {
		id, ok := message["tool_call_id"].(string)
		if !ok || id == "" {
			return fmt.Errorf("tool message is missing tool_call_id")
		}
	}
	rawCalls, exists := message["tool_calls"]
	if !exists {
		return nil
	}
	if role != "assistant" {
		return fmt.Errorf("tool_calls have wrong role")
	}
	calls, ok := rawCalls.([]any)
	if !ok {
		return fmt.Errorf("tool_calls is not an array")
	}
	for _, rawCall := range calls {
		call, okCall := rawCall.(map[string]any)
		if !okCall || call["type"] != "function" {
			return fmt.Errorf("unsupported tool call")
		}
		id, okID := call["id"].(string)
		function, okFunction := call["function"].(map[string]any)
		if !okID || id == "" || !okFunction {
			return fmt.Errorf("invalid tool call")
		}
		name, okName := function["name"].(string)
		_, okArguments := function["arguments"].(string)
		if !okName || name == "" || !okArguments {
			return fmt.Errorf("invalid tool function")
		}
	}
	return nil
}

func validOpenAIRole(role string) bool {
	switch role {
	case "system", "developer", "user", "assistant", "tool":
		return true
	default:
		return false
	}
}

func (view *projection) addTarget(body []byte, path string, messageIndex, blockIndex int, original string) error {
	result := gjson.GetBytes(body, path)
	if result.Type != gjson.String || result.Index < 0 {
		return fmt.Errorf("text slot is not a JSON string")
	}
	start := result.Index
	end := start + len(result.Raw)
	if start < 0 || end > len(body) || !bytes.Equal(body[start:end], []byte(result.Raw)) {
		return fmt.Errorf("text slot span is invalid")
	}
	view.targets = append(view.targets, textTarget{
		start: start, end: end, messageIndex: messageIndex, blockIndex: blockIndex, original: original,
	})
	return nil
}

func (view *projection) validate(rawMessages []json.RawMessage) ([]string, error) {
	if len(rawMessages) != len(view.messages) {
		return nil, fmt.Errorf("message count changed")
	}
	actual := make([]any, len(rawMessages))
	for index, raw := range rawMessages {
		value, errDecode := decodeValue(raw)
		if errDecode != nil {
			return nil, errDecode
		}
		actual[index] = value
	}
	expected, errClone := cloneValues(view.messages)
	if errClone != nil {
		return nil, errClone
	}
	replacements := make([]string, len(view.targets))
	for index, target := range view.targets {
		text, errActual := replaceProjectedText(actual, target, maskedText)
		if errActual != nil {
			return nil, errActual
		}
		if target.original != "" && text == "" {
			return nil, fmt.Errorf("compressed text became empty")
		}
		replacements[index] = text
		if _, errExpected := replaceProjectedText(expected, target, maskedText); errExpected != nil {
			return nil, errExpected
		}
	}
	if !reflect.DeepEqual(expected, actual) {
		return nil, fmt.Errorf("message invariant changed")
	}
	return replacements, nil
}

func replaceProjectedText(messages []any, target textTarget, replacement string) (string, error) {
	if target.messageIndex < 0 || target.messageIndex >= len(messages) {
		return "", fmt.Errorf("message index changed")
	}
	message, okMessage := messages[target.messageIndex].(map[string]any)
	if !okMessage {
		return "", fmt.Errorf("message shape changed")
	}
	if target.blockIndex < 0 {
		text, okText := message["content"].(string)
		if !okText {
			return "", fmt.Errorf("message content type changed")
		}
		message["content"] = replacement
		return text, nil
	}
	blocks, okBlocks := message["content"].([]any)
	if !okBlocks || target.blockIndex >= len(blocks) {
		return "", fmt.Errorf("content block shape changed")
	}
	block, okBlock := blocks[target.blockIndex].(map[string]any)
	if !okBlock {
		return "", fmt.Errorf("content block type changed")
	}
	text, okText := block["text"].(string)
	if !okText {
		return "", fmt.Errorf("content text changed type")
	}
	block["text"] = replacement
	return text, nil
}

func (view *projection) rewrite(body []byte, replacements []string) ([]byte, bool) {
	if len(replacements) != len(view.targets) {
		return body, false
	}
	type edit struct {
		start int
		end   int
		raw   []byte
	}
	edits := make([]edit, 0, len(view.targets))
	for index, target := range view.targets {
		if replacements[index] == target.original {
			continue
		}
		encoded, errMarshal := json.Marshal(replacements[index])
		if errMarshal != nil {
			return body, false
		}
		edits = append(edits, edit{start: target.start, end: target.end, raw: encoded})
	}
	if len(edits) == 0 {
		return body, false
	}
	sort.Slice(edits, func(left, right int) bool { return edits[left].start < edits[right].start })
	outputLength := len(body)
	previousEnd := 0
	for _, current := range edits {
		if current.start < previousEnd || current.end < current.start || current.end > len(body) {
			return body, false
		}
		outputLength += len(current.raw) - (current.end - current.start)
		previousEnd = current.end
	}
	output := make([]byte, 0, outputLength)
	previousEnd = 0
	for _, current := range edits {
		output = append(output, body[previousEnd:current.start]...)
		output = append(output, current.raw...)
		previousEnd = current.end
	}
	output = append(output, body[previousEnd:]...)
	return output, true
}

func decodeObject(raw []byte) (map[string]any, error) {
	value, errValue := decodeValue(raw)
	if errValue != nil {
		return nil, errValue
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("payload root is not an object")
	}
	return object, nil
}

func decodeValue(raw []byte) (any, error) {
	if errUnique := validateUniqueObjectKeys(raw); errUnique != nil {
		return nil, errUnique
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return nil, errDecode
	}
	var trailing any
	if errTrailing := decoder.Decode(&trailing); errTrailing != io.EOF {
		if errTrailing == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, errTrailing
	}
	return value, nil
}

func validateUniqueObjectKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if errValue := consumeJSONValue(decoder); errValue != nil {
		return errValue
	}
	if _, errTrailing := decoder.Token(); errTrailing != io.EOF {
		if errTrailing == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return errTrailing
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, errToken := decoder.Token()
	if errToken != nil {
		return errToken
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, errKey := decoder.Token()
			if errKey != nil {
				return errKey
			}
			key, okKey := keyToken.(string)
			if !okKey {
				return fmt.Errorf("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate object key")
			}
			seen[key] = struct{}{}
			if errValue := consumeJSONValue(decoder); errValue != nil {
				return errValue
			}
		}
	case '[':
		for decoder.More() {
			if errValue := consumeJSONValue(decoder); errValue != nil {
				return errValue
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
	closing, errClosing := decoder.Token()
	if errClosing != nil {
		return errClosing
	}
	want := json.Delim('}')
	if delimiter == '[' {
		want = ']'
	}
	if closing != want {
		return fmt.Errorf("unexpected JSON closing delimiter")
	}
	return nil
}

func cloneValues(values []any) ([]any, error) {
	raw, errMarshal := json.Marshal(values)
	if errMarshal != nil {
		return nil, errMarshal
	}
	value, errDecode := decodeValue(raw)
	if errDecode != nil {
		return nil, errDecode
	}
	cloned, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("message clone is not an array")
	}
	return cloned, nil
}
