package filters

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	gitDiffHunkMaxLines = 100
	gitLogMaxLines      = 200
	statusMaxFiles      = 10
)

var (
	gitCommitLine      = regexp.MustCompile(`(?i)^commit [0-9a-f]{7,40}$`)
	gitGraphCommitLine = regexp.MustCompile(`(?i)^[*|/\\ ]+commit [0-9a-f]{7,40}`)
	gitGraphOneline    = regexp.MustCompile(`(?i)^[*|/\\ ]+([0-9a-f]{7,40}\s+.+)`)
	gitPlainOneline    = regexp.MustCompile(`(?i)^[0-9a-f]{7,40}\s+`)
	gitGraphOnly       = regexp.MustCompile(`^[*|/\\ ]+$`)
	gitAuthorDate      = regexp.MustCompile(`(?i)^[*|/\\ ]*(Author|Date):`)
	gitSubject         = regexp.MustCompile(`^[*|/\\ ]*    \S`)
	gitStatSummary     = regexp.MustCompile(`^\d+ files? changed`)
	longStatus         = regexp.MustCompile(`^\s*(modified|new file|deleted|renamed|both modified):\s+(.+)$`)
)

// GitDiff compacts unified diffs and caps each hunk at 100 displayed lines.
func GitDiff(input string) string {
	result := make([]string, 0)
	currentFile := ""
	added, removed := 0, 0
	inHunk, shown, skipped, truncated := false, 0, 0, false
	flushSkipped := func() {
		if skipped > 0 {
			result = append(result, fmt.Sprintf("  ... (%d lines truncated)", skipped))
			truncated = true
			skipped = 0
		}
	}
	flushCounts := func() {
		if currentFile != "" && (added > 0 || removed > 0) {
			result = append(result, fmt.Sprintf("  +%d -%d", added, removed))
		}
	}

	for _, line := range strings.Split(input, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git"):
			flushSkipped()
			flushCounts()
			parts := strings.Split(line, " b/")
			currentFile = "unknown"
			if len(parts) > 1 {
				currentFile = strings.Join(parts[1:], " b/")
			}
			result = append(result, "\n"+currentFile)
			added, removed, shown = 0, 0, 0
			inHunk = false
		case strings.HasPrefix(line, "@@"):
			flushSkipped()
			inHunk, shown = true, 0
			result = append(result, "  "+line)
		case inHunk:
			changed := strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---")
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				added++
			} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
				removed++
			}
			if changed {
				if shown < gitDiffHunkMaxLines {
					result = append(result, "  "+line)
					shown++
				} else {
					skipped++
				}
			} else if shown > 0 && shown < gitDiffHunkMaxLines && !strings.HasPrefix(line, `\`) {
				result = append(result, "  "+line)
				shown++
			}
		}
		if len(result) >= 500 {
			result = append(result, "\n... (more changes truncated)")
			truncated = true
			break
		}
	}
	flushSkipped()
	flushCounts()
	if truncated {
		result = append(result, "[full diff: rtk git diff --no-compact]")
	}
	return strings.Join(result, "\n")
}

// GitLog retains commit identity, author/date, subject, stats, and an omitted
// marker for embedded diffs while dropping verbose bodies and graph padding.
func GitLog(input string) string {
	if input == "" {
		return ""
	}
	out := make([]string, 0)
	skipped := 0
	inCommit, subjectSeen := false, false
	push := func(line string) {
		if len(out) < gitLogMaxLines {
			out = append(out, line)
		} else {
			skipped++
		}
	}
	for _, raw := range strings.Split(input, "\n") {
		line := strings.TrimRight(raw, "\r\t ")
		trimmed := strings.TrimSpace(line)
		if gitCommitLine.MatchString(trimmed) || gitGraphCommitLine.MatchString(trimmed) {
			inCommit, subjectSeen = true, false
			push(line)
			continue
		}
		if inCommit {
			switch {
			case gitAuthorDate.MatchString(trimmed):
				push(trimmed)
			case trimmed == "":
			case !subjectSeen && gitSubject.MatchString(line):
				push("  Subject: " + trimmed)
				subjectSeen = true
			case gitStatSummary.MatchString(trimmed):
				push("  " + trimmed)
			case strings.HasPrefix(trimmed, "diff --git "):
				push("  ... diff body omitted")
			}
			continue
		}
		if match := gitGraphOneline.FindStringSubmatch(trimmed); len(match) == 2 {
			push(match[1])
		} else if gitPlainOneline.MatchString(trimmed) {
			push(trimmed)
		} else if !(gitGraphOnly.MatchString(trimmed) && strings.ContainsAny(trimmed, "*|/\\")) {
			push(trimmed)
		}
	}
	if skipped > 0 {
		out = append(out, fmt.Sprintf("... (%d more lines)", skipped))
	}
	result := strings.Join(out, "\n")
	if result == "" || len(result) > len(input) {
		return input
	}
	return result
}

// GitStatus summarizes staged, modified, untracked, and conflicting files.
func GitStatus(input string) string {
	if strings.TrimSpace(input) == "" {
		return "Clean working tree"
	}
	branch := ""
	staged, modified, untracked := make([]string, 0), make([]string, 0), make([]string, 0)
	conflicts := 0
	for _, raw := range strings.Split(input, "\n") {
		if strings.HasPrefix(raw, "On branch ") {
			branch = strings.TrimSpace(strings.TrimPrefix(raw, "On branch "))
			continue
		}
		if strings.HasPrefix(raw, "##") {
			branch = strings.TrimSpace(strings.TrimPrefix(raw, "##"))
			continue
		}
		if len(raw) >= 3 && raw[2] == ' ' {
			x, y, file := raw[0], raw[1], raw[3:]
			if x == '?' && y == '?' {
				untracked = append(untracked, file)
				continue
			}
			if strings.ContainsRune("MADRC", rune(x)) {
				staged = append(staged, file)
			} else if x == 'U' {
				conflicts++
			}
			if y == 'M' || y == 'D' {
				modified = append(modified, file)
			}
			continue
		}
		if match := longStatus.FindStringSubmatch(raw); len(match) == 3 {
			kind, file := match[1], strings.TrimSpace(match[2])
			switch kind {
			case "both modified":
				conflicts++
			case "modified", "deleted":
				modified = append(modified, file)
			default:
				staged = append(staged, file)
			}
		}
	}
	var out strings.Builder
	if branch != "" {
		fmt.Fprintf(&out, "* %s\n", branch)
	}
	writeStatusGroup(&out, "+ Staged", staged)
	writeStatusGroup(&out, "~ Modified", modified)
	writeStatusGroup(&out, "? Untracked", untracked)
	if conflicts > 0 {
		fmt.Fprintf(&out, "conflicts: %d files\n", conflicts)
	}
	if len(staged)+len(modified)+len(untracked)+conflicts == 0 {
		out.WriteString("clean — nothing to commit\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

func writeStatusGroup(out *strings.Builder, label string, files []string) {
	if len(files) == 0 {
		return
	}
	fmt.Fprintf(out, "%s: %d files\n", label, len(files))
	limit := min(len(files), statusMaxFiles)
	for _, file := range files[:limit] {
		fmt.Fprintf(out, "   %s\n", file)
	}
	if len(files) > limit {
		fmt.Fprintf(out, "   ... +%d more\n", len(files)-limit)
	}
}
