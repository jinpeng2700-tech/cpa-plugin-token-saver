// Package rtk applies conservative RTK compression to recognized provider tool
// result text without re-encoding the rest of the request payload.
package rtk

import (
	"encoding/json"

	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/protocol"
	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/rtk/filters"
	"github.com/tidwall/gjson"
)

const (
	MinCompressBytes = 500
	MaxRawBytes      = 10 * 1024 * 1024
)

// Apply compresses eligible tool-result text slots. Every rejection path
// returns body byte-for-byte: unknown pairs/shapes, malformed JSON, errors,
// thresholds, filter failures, empty output, and non-shrinking output.
func Apply(body []byte, pair protocol.Pair) []byte {
	view, ok := protocol.View(body, pair)
	if !ok {
		return body
	}
	replacements := make(map[int]string)
	for index, slot := range view.Slots() {
		if slot.Error {
			continue
		}
		candidate, replace := compressText(slot.Text)
		if replace {
			replacements[index] = candidate
		}
	}
	return view.Rewrite(replacements)
}

func compressText(input string) (string, bool) {
	raw := []byte(input)
	if gjson.ValidBytes(raw) && gjson.ParseBytes(raw).IsObject() {
		output := gjson.GetBytes(raw, "output")
		if output.Type == gjson.String && output.Index >= 0 {
			candidate, replace := compressPlainText(output.String())
			if !replace {
				return input, false
			}
			encoded, err := json.Marshal(candidate)
			end := output.Index + len(output.Raw)
			if err != nil || end > len(raw) || string(raw[output.Index:end]) != output.Raw {
				return input, false
			}
			updated := make([]byte, 0, len(raw)-len(output.Raw)+len(encoded))
			updated = append(updated, raw[:output.Index]...)
			updated = append(updated, encoded...)
			updated = append(updated, raw[end:]...)
			return string(updated), true
		}
	}
	return compressPlainText(input)
}

func compressPlainText(input string) (string, bool) {
	size := len([]byte(input))
	if size < MinCompressBytes || size > MaxRawBytes {
		return input, false
	}
	filter := Detect(input)
	if filter.Apply == nil {
		return input, false
	}
	candidate := safelyApply(filter, input)
	return acceptReplacement(input, candidate)
}

func safelyApply(filter filters.Filter, input string) (output string) {
	output = input
	defer func() {
		if recover() != nil {
			output = input
		}
	}()
	if filter.Apply == nil {
		return input
	}
	return filter.Apply(input)
}

func acceptReplacement(original, candidate string) (string, bool) {
	if candidate == "" || len([]byte(candidate)) >= len([]byte(original)) {
		return original, false
	}
	return candidate, true
}
