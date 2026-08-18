package dbusserver

import (
	"regexp"
	"strings"
)

var (
	consoleANSI   = regexp.MustCompile(`(?:\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07]*(?:\x07|\x1b\\))`)
	consoleHome   = regexp.MustCompile(`/(?:home|root)/[^/\s]+`)
	consoleSecret = regexp.MustCompile(`(?i)(password|passwd|token|secret|authorization)(\s*[=:]\s*)\S+`)
)

// sanitizeSoftwareConsoleLine keeps transaction diagnostics useful without
// exposing terminal controls, user names or common credential assignments.
func sanitizeSoftwareConsoleLine(line string) string {
	line = consoleANSI.ReplaceAllString(line, "")
	line = strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\t') || (r >= 0x7f && r <= 0x9f) {
			return -1
		}
		return r
	}, line)
	line = consoleHome.ReplaceAllString(line, "<home>")
	line = consoleSecret.ReplaceAllString(line, "$1$2<redacted>")
	line = strings.TrimSpace(line)
	if len(line) > 4096 {
		line = line[:4096] + "…"
	}
	return line
}
