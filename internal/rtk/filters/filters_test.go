package filters

import (
	"fmt"
	"strings"
	"testing"
)

func TestCoreFiltersCompactRecognizedOutput(t *testing.T) {
	t.Run("git diff", func(t *testing.T) {
		lines := []string{"diff --git a/a.go b/a.go", "@@ -1 +1 @@"}
		for i := 0; i < 140; i++ {
			lines = append(lines, fmt.Sprintf("+line %d", i))
		}
		out := GitDiff(strings.Join(lines, "\n"))
		if !strings.Contains(out, "a.go") || !strings.Contains(out, "lines truncated") {
			t.Fatalf("GitDiff() = %q", out)
		}
	})

	t.Run("git status", func(t *testing.T) {
		out := GitStatus("On branch main\n M one.go\nA  two.go\n?? three.go")
		for _, want := range []string{"* main", "~ Modified", "+ Staged", "? Untracked"} {
			if !strings.Contains(out, want) {
				t.Fatalf("GitStatus() missing %q: %q", want, out)
			}
		}
	})

	t.Run("git log", func(t *testing.T) {
		input := "commit abc1234def5678abc1234def5678abc1234def5\nAuthor: Dev\nDate: now\n\n    Keep subject\n\n    drop body detail"
		out := GitLog(input)
		if !strings.Contains(out, "Keep subject") || strings.Contains(out, "drop body detail") {
			t.Fatalf("GitLog() = %q", out)
		}
	})

	t.Run("grep", func(t *testing.T) {
		lines := make([]string, 0, 20)
		for i := 1; i <= 20; i++ {
			lines = append(lines, fmt.Sprintf("a.go:%d:match %d", i, i))
		}
		out := Grep(strings.Join(lines, "\n"))
		if !strings.Contains(out, "20 matches in 1F") || !strings.Contains(out, "+10") {
			t.Fatalf("Grep() = %q", out)
		}
	})

	t.Run("ls", func(t *testing.T) {
		input := "total 3\ndrwxr-xr-x 1 u g 1 Jan 1 12:00 node_modules\ndrwxr-xr-x 1 u g 1 Jan 1 12:00 src\n-rw-r--r-- 1 u g 2048 Jan 1 12:00 main.go"
		out := LS(input)
		if strings.Contains(out, "node_modules") || !strings.Contains(out, "src/") || !strings.Contains(out, "2.0K") {
			t.Fatalf("LS() = %q", out)
		}
	})

	t.Run("tree", func(t *testing.T) {
		out := Tree(".\n├── src\n│   └── main.go\n1 directory, 1 file\n")
		if !strings.Contains(out, "main.go") || strings.Contains(out, "directory") {
			t.Fatalf("Tree() = %q", out)
		}
	})
}

func TestRegisteredExtraFilters(t *testing.T) {
	t.Run("find windows", func(t *testing.T) {
		out := Find("C:\\src\\a.go\nC:\\src\\b.go\nC:\\src\\c.go")
		if !strings.Contains(out, "C:/src/") || strings.Contains(out, `\`) {
			t.Fatalf("Find() = %q", out)
		}
	})

	t.Run("dedup log", func(t *testing.T) {
		out := DedupLog(strings.Repeat("same\n", 20))
		if !strings.Contains(out, "duplicate lines") {
			t.Fatalf("DedupLog() = %q", out)
		}
	})

	t.Run("smart truncate", func(t *testing.T) {
		input := numberedLines(400, "line ")
		out := SmartTruncate(input)
		if !strings.Contains(out, "lines truncated") || !strings.Contains(out, "line 0") || !strings.Contains(out, "line 399") {
			t.Fatalf("SmartTruncate() did not retain head/tail")
		}
	})

	t.Run("read numbered", func(t *testing.T) {
		input := numberedLines(400, "  ")
		out := ReadNumbered(input)
		if !strings.Contains(out, "file continues") || !strings.Contains(out, "399") {
			t.Fatalf("ReadNumbered() did not retain head/tail")
		}
	})

	t.Run("search list", func(t *testing.T) {
		lines := []string{"Result of search in '/tmp' (total 20 files):"}
		for i := 0; i < 20; i++ {
			lines = append(lines, fmt.Sprintf("- src/file-%02d.go", i))
		}
		out := SearchList(strings.Join(lines, "\n"))
		if !strings.Contains(out, "20 files in 1 dirs") || !strings.Contains(out, "+10") {
			t.Fatalf("SearchList() = %q", out)
		}
	})

	t.Run("build output", func(t *testing.T) {
		input := strings.Repeat("   Compiling dependency v1\n", 20) + "    Finished dev profile in 2s"
		out := BuildOutput(input)
		if !strings.Contains(out, "Compiled 20 packages") || !strings.Contains(out, "Finished") || len(out) >= len(input) {
			t.Fatalf("BuildOutput() = %q", out)
		}
	})
}

func numberedLines(count int, prefix string) string {
	lines := make([]string, count)
	for i := range lines {
		lines[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return strings.Join(lines, "\n")
}
