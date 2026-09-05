package probe

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	pluginIDPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	pluginVersionPattern = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]*$`)
)

// parsePluginFilename mirrors the Linux subset of CLIProxyAPI's discovery
// rules. Keeping this exact is load-bearing: token-saver-v1.2.4.so is plugin
// ID token-saver, while cpa-plugin-token-saver-v1.2.4.so is a different ID.
func parsePluginFilename(path string) (string, string, bool) {
	base := filepath.Base(path)
	if !strings.HasSuffix(strings.ToLower(base), ".so") {
		return "", "", false
	}
	name := base[:len(base)-len(".so")]
	id := name
	version := ""
	if versionIndex := strings.LastIndex(name, "-v"); versionIndex > 0 {
		candidateID := name[:versionIndex]
		candidateVersion := name[versionIndex+2:]
		if pluginIDPattern.MatchString(candidateID) && pluginVersionPattern.MatchString(candidateVersion) && !strings.HasPrefix(candidateVersion, "v") {
			id = candidateID
			version = candidateVersion
		}
	}
	if !pluginIDPattern.MatchString(id) {
		return "", "", false
	}
	return id, version, true
}

func markerCount(body []byte) int {
	return strings.Count(string(body), CavemanMarker)
}
