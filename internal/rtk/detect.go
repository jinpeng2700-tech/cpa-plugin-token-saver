package rtk

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/router-for-me/cpa-plugin-token-saver/internal/rtk/filters"
)

const detectWindowBytes = 1024

var (
	detectGitLog       = regexp.MustCompile(`(?im)^[*|/\\ ]*commit [0-9a-f]{7,40}$`)
	detectGitDiff      = regexp.MustCompile(`(?m)^diff --git |^@@ `)
	detectGitStatus    = regexp.MustCompile(`(?m)^On branch |^nothing to commit|^Changes (not |to be )|^Untracked files:`)
	detectBuild        = regexp.MustCompile(`(?im)^(npm (warn|error|ERR!)|yarn (warn|error)|\s*Compiling\s+\S+|\s*Downloading\s+\S+|added \d+ package|\[ERROR\]|BUILD (SUCCESS|FAILED)|\s*Finished\s+|Successfully (installed|built)|ERROR:)`)
	detectTree         = regexp.MustCompile(`[├└]──|│  `)
	detectLSRow        = regexp.MustCompile(`^[-dlbcps][rwx-]{9}`)
	detectLSTotal      = regexp.MustCompile(`^total \d+$`)
	detectSearchList   = regexp.MustCompile(`^Result of search in '[^']*' \(total \d+ files?\):`)
	detectReadNumbered = regexp.MustCompile(`^\s*\d+\|`)
)

// Detect chooses the first RTK filter matching the upstream detection order.
func Detect(text string) filters.Filter {
	head := text
	if len(head) > detectWindowBytes {
		end := detectWindowBytes
		for end > 0 && !utf8.RuneStart(head[end]) {
			end--
		}
		head = head[:end]
	}
	if detectGitLog.MatchString(head) {
		return filters.Resolve("git-log")
	}
	if detectGitDiff.MatchString(head) {
		return filters.Resolve("git-diff")
	}
	if detectGitStatus.MatchString(head) {
		return filters.Resolve("git-status")
	}
	if detectBuild.MatchString(head) {
		return filters.Resolve("build-output")
	}
	if mostlyPorcelain(head) {
		return filters.Resolve("git-status")
	}

	lines := strings.Split(head, "\n")
	nonEmpty := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty = append(nonEmpty, line)
		}
	}
	for _, line := range nonEmpty[:min(len(nonEmpty), 5)] {
		if grepLine(line) {
			return filters.Resolve("grep")
		}
	}
	if len(nonEmpty) >= 3 {
		allPaths := true
		for _, line := range nonEmpty {
			if !pathLike(line) {
				allPaths = false
				break
			}
		}
		if allPaths {
			return filters.Resolve("find")
		}
	}
	if detectTree.MatchString(head) {
		return filters.Resolve("tree")
	}
	lsRows := 0
	for _, line := range lines {
		if detectLSRow.MatchString(line) {
			lsRows++
		}
	}
	if detectLSTotal.MatchString(head) || lsRows >= 3 {
		return filters.Resolve("ls")
	}
	if detectSearchList.MatchString(head) {
		return filters.Resolve("search-list")
	}
	if len(lines) >= 250 && mostlyLineNumbered(lines) {
		return filters.Resolve("read-numbered")
	}
	if len(nonEmpty) >= 5 {
		return filters.Resolve("dedup-log")
	}
	if len(strings.Split(text, "\n")) >= 250 {
		return filters.Resolve("smart-truncate")
	}
	return filters.Filter{}
}

func grepLine(line string) bool {
	start := 0
	if len(line) >= 3 && isASCIIAlpha(line[0]) && line[1] == ':' && (line[2] == '\\' || line[2] == '/') {
		start = 2
	}
	firstRelative := strings.IndexByte(line[start:], ':')
	if firstRelative < 0 {
		return false
	}
	first := start + firstRelative
	secondRelative := strings.IndexByte(line[first+1:], ':')
	if secondRelative < 0 {
		return false
	}
	lineNumber := line[first+1 : first+1+secondRelative]
	if lineNumber == "" {
		return false
	}
	for _, value := range []byte(lineNumber) {
		if value < '0' || value > '9' {
			return false
		}
	}
	return true
}

func pathLike(line string) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) >= 3 && isASCIIAlpha(trimmed[0]) && trimmed[1] == ':' && (trimmed[2] == '\\' || trimmed[2] == '/') {
		return true
	}
	if strings.Contains(trimmed, ":") {
		return false
	}
	return strings.HasPrefix(trimmed, ".") || strings.HasPrefix(trimmed, "/") || strings.ContainsAny(trimmed, `/\`)
}

func mostlyPorcelain(head string) bool {
	lines := make([]string, 0)
	for _, line := range strings.Split(head, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) < 3 {
		return false
	}
	hits := 0
	for _, line := range lines {
		if len(line) >= 4 && line[2] == ' ' && strings.ContainsRune(" MADRCU?!", rune(line[0])) && strings.ContainsRune(" MADRCU?!", rune(line[1])) {
			hits++
		}
	}
	return float64(hits)/float64(len(lines)) >= 0.6
}

func mostlyLineNumbered(lines []string) bool {
	hits, nonEmpty := 0, 0
	for _, line := range lines[:min(len(lines), 100)] {
		if line == "" {
			continue
		}
		nonEmpty++
		if detectReadNumbered.MatchString(line) {
			hits++
		}
	}
	return nonEmpty >= 5 && float64(hits)/float64(nonEmpty) >= 0.7
}

func isASCIIAlpha(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}
