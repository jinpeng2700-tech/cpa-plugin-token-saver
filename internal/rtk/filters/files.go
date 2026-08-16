package filters

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	grepPerFileMax = 10
	findPerDirMax  = 10
	findDirMax     = 20
)

type grepMatch struct {
	line    string
	content string
}

// Grep groups file:line:content matches and caps each file at ten entries.
func Grep(input string) string {
	byFile := make(map[string][]grepMatch)
	total := 0
	for _, line := range strings.Split(input, "\n") {
		file, lineNumber, content, ok := splitGrepLine(line)
		if !ok {
			continue
		}
		total++
		byFile[file] = append(byFile[file], grepMatch{line: lineNumber, content: content})
	}
	if total == 0 {
		return input
	}
	files := make([]string, 0, len(byFile))
	for file := range byFile {
		files = append(files, file)
	}
	sort.Strings(files)
	var out strings.Builder
	fmt.Fprintf(&out, "%d matches in %dF:\n\n", total, len(files))
	for _, file := range files {
		matches := byFile[file]
		fmt.Fprintf(&out, "[file] %s (%d):\n", file, len(matches))
		for _, match := range matches[:min(len(matches), grepPerFileMax)] {
			fmt.Fprintf(&out, "  %4s: %s\n", match.line, strings.TrimSpace(match.content))
		}
		if len(matches) > grepPerFileMax {
			fmt.Fprintf(&out, "  +%d\n", len(matches)-grepPerFileMax)
		}
		out.WriteByte('\n')
	}
	return out.String()
}

func splitGrepLine(line string) (string, string, string, bool) {
	start := 0
	if len(line) >= 3 && ((line[0] >= 'A' && line[0] <= 'Z') || (line[0] >= 'a' && line[0] <= 'z')) && line[1] == ':' && (line[2] == '\\' || line[2] == '/') {
		start = 2
	}
	firstRelative := strings.IndexByte(line[start:], ':')
	if firstRelative < 0 {
		return "", "", "", false
	}
	first := start + firstRelative
	secondRelative := strings.IndexByte(line[first+1:], ':')
	if secondRelative < 0 {
		return "", "", "", false
	}
	second := first + 1 + secondRelative
	lineNumber := line[first+1 : second]
	if _, err := strconv.ParseUint(lineNumber, 10, 64); err != nil {
		return "", "", "", false
	}
	return line[:first], lineNumber, line[second+1:], true
}

// Find groups Unix and Windows paths by parent directory.
func Find(input string) string {
	lines := nonEmptyLines(input)
	if len(lines) == 0 {
		return input
	}
	byDir := make(map[string][]string)
	for _, path := range lines {
		last := max(strings.LastIndex(path, "/"), strings.LastIndex(path, `\`))
		dir, base := ".", path
		if last >= 0 {
			dir, base = path[:last], path[last+1:]
			if dir == "" {
				dir = "/"
			}
		}
		byDir[dir] = append(byDir[dir], base)
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	var out strings.Builder
	fmt.Fprintf(&out, "%d files in %d dirs:\n\n", len(lines), len(dirs))
	for _, dir := range dirs[:min(len(dirs), findDirMax)] {
		files := byDir[dir]
		fmt.Fprintf(&out, "%s/  (%d)\n", strings.ReplaceAll(dir, `\`, "/"), len(files))
		for _, file := range files[:min(len(files), findPerDirMax)] {
			fmt.Fprintf(&out, "  %s\n", file)
		}
		if len(files) > findPerDirMax {
			fmt.Fprintf(&out, "  +%d\n", len(files)-findPerDirMax)
		}
	}
	if len(dirs) > findDirMax {
		fmt.Fprintf(&out, "\n+%d more dirs\n", len(dirs)-findDirMax)
	}
	return out.String()
}

// SearchList compacts Cursor's explicit "Result of search" path listing.
func SearchList(input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "Result of search in '") {
		return input
	}
	paths := make([]string, 0)
	for _, raw := range lines[1:] {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "- ") {
			paths = append(paths, strings.TrimPrefix(trimmed, "- "))
		}
	}
	if len(paths) == 0 {
		return input
	}
	byDir := make(map[string][]string)
	for _, path := range paths {
		dir, name := filepath.ToSlash(filepath.Dir(path)), filepath.Base(path)
		if dir == "" {
			dir = "."
		}
		byDir[dir] = append(byDir[dir], name)
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	var out strings.Builder
	fmt.Fprintf(&out, "%s\n%d files in %d dirs:\n\n", lines[0], len(paths), len(dirs))
	for _, dir := range dirs[:min(len(dirs), findDirMax)] {
		names := byDir[dir]
		fmt.Fprintf(&out, "%s/ (%d):\n", dir, len(names))
		for _, name := range names[:min(len(names), findPerDirMax)] {
			fmt.Fprintf(&out, "  %s\n", name)
		}
		if len(names) > findPerDirMax {
			fmt.Fprintf(&out, "  +%d\n", len(names)-findPerDirMax)
		}
		out.WriteByte('\n')
	}
	if len(dirs) > findDirMax {
		fmt.Fprintf(&out, "+%d more dirs\n", len(dirs)-findDirMax)
	}
	return strings.TrimRight(out.String(), "\n")
}

func nonEmptyLines(input string) []string {
	result := make([]string, 0)
	for _, line := range strings.Split(input, "\n") {
		if strings.TrimSpace(line) != "" {
			result = append(result, line)
		}
	}
	return result
}
