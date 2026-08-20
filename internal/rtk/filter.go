// Package rtk applies conservative RTK compression to recognized provider tool
// result text without re-encoding the rest of the request payload.
package rtk

import (
	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/protocol"
	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/rtk/filters"
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
		size := len([]byte(slot.Text))
		if size < MinCompressBytes || size > MaxRawBytes {
			continue
		}
		filter := Detect(slot.Text)
		if filter.Apply == nil {
			continue
		}
		candidate := safelyApply(filter, slot.Text)
		if accepted, replace := acceptReplacement(slot.Text, candidate); replace {
			replacements[index] = accepted
		}
	}
	return view.Rewrite(replacements)
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
