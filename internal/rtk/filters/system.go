package filters

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	dedupLineMax       = 2000
	treeMaxLines       = 200
	smartHeadLines     = 120
	smartTailLines     = 60
	smartMinLines      = 250
	lsExtensionSummary = 5
)

var (
	lsDate     = regexp.MustCompile(`\s+(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d{1,2}\s+(\d{4}|\d{2}:\d{2})\s+`)
	noiseNames = map[string]struct{}{
		"node_modules": {}, ".git": {}, "target": {}, "__pycache__": {}, ".next": {}, "dist": {}, "build": {}, ".cache": {}, ".turbo": {}, ".vercel": {}, ".pytest_cache": {}, ".mypy_cache": {}, ".tox": {}, ".venv": {}, "venv": {}, "env": {}, "coverage": {}, ".nyc_output": {}, ".DS_Store": {}, "Thumbs.db": {}, ".idea": {}, ".vscode": {}, ".vs": {}, ".eggs": {},
	}
)

// DedupLog collapses consecutive duplicate lines and repeated blank lines.
func DedupLog(input string) string {
	lines := strings.Split(input, "\n")
	out := make([]string, 0, min(len(lines), dedupLineMax))
	previous := ""
	havePrevious, run, blankStreak := false, 0, 0
	flush := func() {
		if havePrevious && run > 1 {
			out = append(out, fmt.Sprintf("  ... (%d duplicate lines)", run-1))
		}
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if blankStreak == 0 {
				out = append(out, line)
			}
			blankStreak++
			flush()
			havePrevious, run = false, 0
			continue
		}
		blankStreak = 0
		if havePrevious && line == previous {
			run++
			continue
		}
		flush()
		out = append(out, line)
		previous, havePrevious, run = line, true, 1
		if len(out) >= dedupLineMax {
			out = append(out, fmt.Sprintf("... (truncated at %d lines)", dedupLineMax))
			return strings.Join(out, "\n")
		}
	}
	flush()
	return strings.Join(out, "\n")
}

// Tree removes command summaries and caps excessively long trees.
func Tree(input string) string {
	filtered := make([]string, 0)
	for _, line := range strings.Split(input, "\n") {
		if strings.Contains(line, "director") && strings.Contains(line, "file") {
			continue
		}
		if len(filtered) == 0 && strings.TrimSpace(line) == "" {
			continue
		}
		filtered = append(filtered, line)
	}
	for len(filtered) > 0 && strings.TrimSpace(filtered[len(filtered)-1]) == "" {
		filtered = filtered[:len(filtered)-1]
	}
	if len(filtered) > treeMaxLines {
		return strings.Join(filtered[:treeMaxLines], "\n") + fmt.Sprintf("\n... +%d more lines", len(filtered)-treeMaxLines)
	}
	return strings.Join(filtered, "\n")
}

// SmartTruncate keeps a stable head and tail around a middle omission marker.
func SmartTruncate(input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) < smartMinLines {
		return input
	}
	cut := len(lines) - smartHeadLines - smartTailLines
	out := append([]string(nil), lines[:smartHeadLines]...)
	out = append(out, fmt.Sprintf("... +%d lines truncated", cut))
	out = append(out, lines[len(lines)-smartTailLines:]...)
	return strings.Join(out, "\n")
}

// ReadNumbered applies SmartTruncate with a file-specific omission marker.
func ReadNumbered(input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) < smartMinLines {
		return input
	}
	cut := len(lines) - smartHeadLines - smartTailLines
	out := append([]string(nil), lines[:smartHeadLines]...)
	out = append(out, fmt.Sprintf("... +%d lines truncated (file continues)", cut))
	out = append(out, lines[len(lines)-smartTailLines:]...)
	return strings.Join(out, "\n")
}

type lsEntry struct {
	name string
	size int64
	dir  bool
}

// LS strips permissions and ownership from long listings, preserving names and
// compact sizes while omitting common dependency/cache noise.
func LS(input string) string {
	entries := make([]lsEntry, 0)
	for _, line := range strings.Split(input, "\n") {
		if line == "" || strings.HasPrefix(line, "total ") {
			continue
		}
		match := lsDate.FindStringIndex(line)
		if match == nil {
			continue
		}
		name := line[match[1]:]
		if name == "." || name == ".." {
			continue
		}
		if _, noise := noiseNames[name]; noise {
			continue
		}
		fields := strings.Fields(line[:match[0]])
		if len(fields) < 4 || len(fields[0]) == 0 {
			continue
		}
		size := int64(0)
		for i := len(fields) - 1; i >= 0; i-- {
			if parsed, err := strconv.ParseInt(fields[i], 10, 64); err == nil {
				size = parsed
				break
			}
		}
		kind := fields[0][0]
		if kind == 'd' || kind == '-' || kind == 'l' {
			entries = append(entries, lsEntry{name: name, size: size, dir: kind == 'd'})
		}
	}
	if len(entries) == 0 {
		return input
	}
	var out strings.Builder
	extensions := make(map[string]int)
	files, dirs := 0, 0
	for _, entry := range entries {
		if entry.dir {
			dirs++
			fmt.Fprintf(&out, "%s/\n", entry.name)
			continue
		}
		files++
		extension := "no ext"
		if dot := strings.LastIndex(entry.name, "."); dot > 0 {
			extension = entry.name[dot:]
		}
		extensions[extension]++
		fmt.Fprintf(&out, "%s  %s\n", entry.name, humanSize(entry.size))
	}
	fmt.Fprintf(&out, "\nSummary: %d files, %d dirs", files, dirs)
	type count struct {
		extension string
		value     int
	}
	counts := make([]count, 0, len(extensions))
	for extension, value := range extensions {
		counts = append(counts, count{extension, value})
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i].value > counts[j].value })
	if len(counts) > 0 {
		parts := make([]string, 0, min(len(counts), lsExtensionSummary))
		for _, item := range counts[:min(len(counts), lsExtensionSummary)] {
			parts = append(parts, fmt.Sprintf("%d %s", item.value, item.extension))
		}
		if len(counts) > lsExtensionSummary {
			parts = append(parts, fmt.Sprintf("+%d more", len(counts)-lsExtensionSummary))
		}
		fmt.Fprintf(&out, " (%s)", strings.Join(parts, ", "))
	}
	return out.String()
}

func humanSize(size int64) string {
	if size >= 1_048_576 {
		return fmt.Sprintf("%.1fM", float64(size)/1_048_576)
	}
	if size >= 1024 {
		return fmt.Sprintf("%.1fK", float64(size)/1024)
	}
	return fmt.Sprintf("%dB", size)
}
