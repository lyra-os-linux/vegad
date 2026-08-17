package dbusserver

import (
	"os"
	"os/exec"
	"strings"

	"github.com/lyraos/vegad/internal/distro"
)

// progressFunc reports coarse (stage-based, not byte-accurate) progress for
// a running package/kernel/hardware transaction — same shape as
// distro.ProgressFunc, aliased so calls into a distro.*Backend never need an
// explicit conversion.
type progressFunc = distro.ProgressFunc

// packageProgressFunc reports fine-grained per-package progress for a
// software transaction — same shape as distro.PackageProgressFunc, aliased
// for the same reason as progressFunc above.
type packageProgressFunc = distro.PackageProgressFunc

func commandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// systemCommand limits the relaxed umask to Zypper. The daemon keeps its
// restrictive UMask=0077 for credentials and other administrative files,
// while libzypp metadata remains reusable by unprivileged read-only clients.
func systemCommand(name string, args ...string) *exec.Cmd {
	if name == "zypper" {
		wrapped := append([]string{"-c", `umask 0022; exec /usr/bin/zypper "$@"`, "vega-zypper"}, args...)
		return exec.Command("/bin/sh", wrapped...)
	}
	return exec.Command(name, args...)
}

func runCommandOutput(name string, args ...string) (string, error) {
	out, err := systemCommand(name, args...).CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), err
	}
	return strings.TrimSpace(string(out)), nil
}

func runCommand(name string, args ...string) error {
	_, err := runCommandOutput(name, args...)
	return err
}

func readTrimmedFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func normalizeWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
