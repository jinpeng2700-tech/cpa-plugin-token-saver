package filters

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	cargoContinuation = regexp.MustCompile(`^\s*(-->|\||\d+\s*\||=)`)
	buildCompiling    = regexp.MustCompile(`(?i)^\s*Compiling\s+\S+`)
	buildDownloading  = regexp.MustCompile(`(?i)^\s*(Downloading\s+\S+|Fetching\s+)`)
	buildSummary      = regexp.MustCompile(`(?i)^(added|removed|changed|audited|installed)\s+\d+\s+package|^\s*Finished\s+|^BUILD SUCCESS|^\d+\s+(vulnerabilities|packages?|warnings?|errors?)|^Successfully (installed|built)|^To address .* issues|^Run ` + "`" + `npm (audit|fund)` + "`" + `|packages are looking for funding`)
)

// BuildOutput keeps errors, a bounded warning/deprecation set, and summaries
// while collapsing package compilation/download progress.
func BuildOutput(input string) string {
	if input == "" {
		return input
	}
	errors, warnings, deprecations := make([]string, 0), make([]string, 0), make([]string, 0)
	summaries := make([]string, 0)
	compiling, downloading, inCargoError := 0, 0, false
	for _, line := range strings.Split(input, "\n") {
		trimmed := strings.TrimSpace(line)
		if inCargoError {
			if trimmed == "" {
				inCargoError = false
				continue
			}
			if cargoContinuation.MatchString(line) {
				errors = append(errors, line)
				continue
			}
			inCargoError = false
		}
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(lower, "npm err!") || strings.HasPrefix(lower, "npm error") || strings.HasPrefix(lower, "yarn error"):
			errors = append(errors, line)
		case strings.HasPrefix(lower, "npm warn deprecated"):
			deprecations = append(deprecations, line)
		case strings.HasPrefix(lower, "npm warn") || strings.HasPrefix(lower, "yarn warn"):
			warnings = append(warnings, line)
		case strings.HasPrefix(lower, "error[") || strings.HasPrefix(lower, "error:") || strings.HasPrefix(lower, "error -->"):
			errors = append(errors, line)
			inCargoError = true
		case strings.HasPrefix(lower, "warning[") || strings.HasPrefix(lower, "warning:") || strings.HasPrefix(lower, "warning -->"):
			warnings = append(warnings, line)
			inCargoError = true
		case strings.HasPrefix(lower, "error:") || strings.HasPrefix(lower, "[error]") || strings.HasPrefix(lower, "build failed"):
			errors = append(errors, line)
		case strings.HasPrefix(lower, "[warning]"):
			warnings = append(warnings, line)
		case buildCompiling.MatchString(trimmed):
			compiling++
		case buildDownloading.MatchString(trimmed):
			downloading++
		case buildSummary.MatchString(trimmed):
			summaries = append(summaries, line)
		}
	}
	var out strings.Builder
	for _, line := range deprecations[:min(len(deprecations), 3)] {
		fmt.Fprintln(&out, line)
	}
	if len(deprecations) > 3 {
		fmt.Fprintf(&out, "... +%d more deprecated packages\n", len(deprecations)-3)
	}
	if compiling > 0 {
		fmt.Fprintf(&out, "Compiled %d packages\n", compiling)
	}
	if downloading > 0 {
		fmt.Fprintf(&out, "Downloaded %d packages\n", downloading)
	}
	for _, line := range errors {
		fmt.Fprintln(&out, line)
	}
	for _, line := range warnings[:min(len(warnings), 5)] {
		fmt.Fprintln(&out, line)
	}
	if len(warnings) > 5 {
		fmt.Fprintf(&out, "... +%d more warnings\n", len(warnings)-5)
	}
	for _, line := range summaries {
		fmt.Fprintln(&out, line)
	}
	result := strings.TrimRight(out.String(), "\n")
	if result == "" {
		return input
	}
	return result
}
