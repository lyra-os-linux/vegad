package distro

import (
	"os"
	"path/filepath"
)

// LogoPath resolves the running distro's own logo/icon file, following the
// freedesktop os-release LOGO field (an icon-theme name, e.g.
// "distributor-logo-Leap") through the standard hicolor theme lookup — the
// same convention desktop environments use to show the vendor logo on their
// own "About this system" screen. Returns "" if nothing resolves, so
// callers can fall back to a generic icon instead of a broken image.
func LogoPath() string {
	var candidates []string
	if fields, err := parseOSRelease(osReleasePath); err == nil && fields["LOGO"] != "" {
		candidates = append(candidates, fields["LOGO"])
	}
	if id, err := Detect(); err == nil {
		candidates = append(candidates, id.String()+"-logo")
	}
	candidates = append(candidates, "distributor-logo")

	dirs := []string{
		"/usr/share/icons/hicolor/scalable/apps",
		"/usr/share/icons/hicolor/256x256/apps",
		"/usr/share/icons/hicolor/128x128/apps",
		"/usr/share/icons/hicolor/64x64/apps",
		"/usr/share/icons/hicolor/48x48/apps",
		"/usr/share/pixmaps",
	}
	exts := []string{".svg", ".png"}

	for _, name := range candidates {
		for _, dir := range dirs {
			for _, ext := range exts {
				candidate := filepath.Join(dir, name+ext)
				if _, err := os.Stat(candidate); err == nil {
					return candidate
				}
			}
		}
	}
	return ""
}
