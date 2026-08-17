package filters

import (
	"fmt"
	"strings"
	"testing"
)

func TestGitStatusCompactsLongFormSections(t *testing.T) {
	untrackedInput, untrackedPaths := longStatusFixture(
		"Untracked files:",
		`  (use "git add <file>..." to include in what will be committed)`,
		longStatusPathEntries("", "untracked", 12),
		`nothing added to commit but untracked files present (use "git add" to track)`,
	)
	stagedInput, stagedPaths := longStatusFixture(
		"Changes to be committed:",
		`  (use "git restore --staged <file>..." to unstage)`,
		longStatusPathEntries("modified", "staged", 12),
		"",
	)
	modifiedInput, modifiedPaths := longStatusFixture(
		"Changes not staged for commit:",
		strings.Join([]string{
			`  (use "git add <file>..." to update what will be committed)`,
			`  (use "git restore <file>..." to discard changes in working directory)`,
		}, "\n"),
		longStatusPathEntries("typechange", "modified", 12),
		`no changes added to commit (use "git add" and/or "git commit -a")`,
	)
	conflictEntries := make([]string, 12)
	conflictPaths := make([]string, 12)
	conflictKinds := []string{"deleted by us", "added by them", "both modified", "both added"}
	for i := range conflictEntries {
		conflictPaths[i] = fmt.Sprintf("conflicts/component-with-a-descriptive-name-%02d.go", i)
		conflictEntries[i] = fmt.Sprintf("\t%s:   %s", conflictKinds[i%len(conflictKinds)], conflictPaths[i])
	}
	conflictInput, _ := longStatusFixture(
		"Unmerged paths:",
		`  (use "git add <file>..." to mark resolution)`,
		conflictEntries,
		`no changes added to commit (use "git add" and/or "git commit -a")`,
	)
	cleanBranch := "release/" + strings.Repeat("long-status-fixture/", 28) + "clean"
	cleanInput := fmt.Sprintf("On branch %s\nYour branch is up to date with 'origin/%s'.\n\nnothing to commit, working tree clean\n", cleanBranch, cleanBranch)

	tests := []struct {
		name         string
		input        string
		want         string
		orderedPaths []string
		dirty        bool
	}{
		{name: "untracked", input: untrackedInput, want: "? Untracked: 12 files", orderedPaths: untrackedPaths[:3], dirty: true},
		{name: "staged", input: stagedInput, want: "+ Staged: 12 files", orderedPaths: stagedPaths[:3], dirty: true},
		{name: "modified", input: modifiedInput, want: "~ Modified: 12 files", orderedPaths: modifiedPaths[:3], dirty: true},
		{name: "unmerged conflicts", input: conflictInput, want: "conflicts: 12 files", dirty: true},
		{name: "explicit clean", input: cleanInput, want: "clean — nothing to commit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.input) <= 500 {
				t.Fatalf("fixture must exercise RTK's compression threshold: got %d bytes", len(tt.input))
			}

			got := GitStatus(tt.input)
			if got == tt.input || len(got) >= len(tt.input) {
				t.Fatalf("GitStatus() did not compact %d-byte long status: %q", len(tt.input), got)
			}
			if !strings.Contains(got, tt.want) {
				t.Fatalf("GitStatus() missing %q: %q", tt.want, got)
			}
			if tt.dirty && strings.Contains(strings.ToLower(got), "clean") {
				t.Fatalf("GitStatus() inverted dirty status to clean: %q", got)
			}
			assertStringsInOrder(t, got, tt.orderedPaths)
		})
	}
}

func TestGitStatusReturnsOriginalForAmbiguousLongFormChanges(t *testing.T) {
	input, _ := longStatusFixture(
		"Changes not staged for commit:",
		`  (use "git add <file>..." to update what will be committed)`,
		longStatusPathEntries("future status", "ambiguous", 12),
		`no changes added to commit (use "git add" and/or "git commit -a")`,
	)
	if len(input) <= 500 {
		t.Fatalf("fixture must exercise RTK's compression threshold: got %d bytes", len(input))
	}
	if got := GitStatus(input); got != input {
		t.Fatalf("GitStatus() must not guess when a change section is ambiguous:\n%s", got)
	}

	for _, input := range []string{"", "\n"} {
		if got := GitStatus(input); got != input {
			t.Fatalf("GitStatus(%q) = %q; empty output is not explicit clean status", input, got)
		}
	}
}

func TestGitStatusPreservesPorcelainPathsAndOrder(t *testing.T) {
	input := strings.Join([]string{
		"## feature/path-order...origin/feature/path-order",
		"A  staged/first path.go",
		"R  staged/old name.go -> staged/second name.go",
		" M modified/first path.go",
		" D modified/second path.go",
		"?? untracked/first path.go",
		"?? untracked/second path.go",
	}, "\n")

	got := GitStatus(input)
	assertStringsInOrder(t, got, []string{"staged/first path.go", "staged/old name.go -> staged/second name.go"})
	assertStringsInOrder(t, got, []string{"modified/first path.go", "modified/second path.go"})
	assertStringsInOrder(t, got, []string{"untracked/first path.go", "untracked/second path.go"})
}

func longStatusFixture(section, hints string, entries []string, footer string) (string, []string) {
	lines := []string{
		"On branch feature/long-status-fixture",
		"Your branch is up to date with 'origin/feature/long-status-fixture'.",
		"",
		section,
		hints,
	}
	lines = append(lines, entries...)
	if footer != "" {
		lines = append(lines, "", footer)
	}

	paths := make([]string, len(entries))
	for i, entry := range entries {
		if _, path, ok := strings.Cut(strings.TrimSpace(entry), ":   "); ok {
			paths[i] = path
		} else {
			paths[i] = strings.TrimSpace(entry)
		}
	}
	return strings.Join(lines, "\n") + "\n", paths
}

func longStatusPathEntries(kind, directory string, count int) []string {
	entries := make([]string, count)
	for i := range entries {
		path := fmt.Sprintf("%s/component-with-a-descriptive-name-%02d.go", directory, i)
		if kind == "" {
			entries[i] = "\t" + path
		} else {
			entries[i] = fmt.Sprintf("\t%s:   %s", kind, path)
		}
	}
	return entries
}

func assertStringsInOrder(t *testing.T, text string, values []string) {
	t.Helper()
	previous := -1
	for _, value := range values {
		index := strings.Index(text, value)
		if index < 0 {
			t.Fatalf("output missing path %q: %q", value, text)
		}
		if index <= previous {
			t.Fatalf("paths are out of order in output: %q", text)
		}
		previous = index
	}
}
