// Package filters contains the Apache-2.0 RTK output filters ported from
// 9router/open-sse. Filters are pure string transforms; the caller owns all
// size and no-growth safety checks.
package filters

// Filter is one named output compressor.
type Filter struct {
	Name  string
	Apply func(string) string
}

var registry = []Filter{
	{Name: "git-diff", Apply: GitDiff},
	{Name: "git-status", Apply: GitStatus},
	{Name: "git-log", Apply: GitLog},
	{Name: "grep", Apply: Grep},
	{Name: "find", Apply: Find},
	{Name: "dedup-log", Apply: DedupLog},
	{Name: "ls", Apply: LS},
	{Name: "tree", Apply: Tree},
	{Name: "smart-truncate", Apply: SmartTruncate},
	{Name: "read-numbered", Apply: ReadNumbered},
	{Name: "search-list", Apply: SearchList},
	{Name: "build-output", Apply: BuildOutput},
}

// Names returns the stable registry order used by the upstream RTK port.
func Names() []string {
	names := make([]string, len(registry))
	for i, filter := range registry {
		names[i] = filter.Name
	}
	return names
}

// Resolve returns a registered filter. rg/fd mirror RTK's command aliases.
func Resolve(name string) Filter {
	if name == "rg" {
		name = "grep"
	} else if name == "fd" {
		name = "find"
	}
	for _, filter := range registry {
		if filter.Name == name {
			return filter
		}
	}
	return Filter{}
}
