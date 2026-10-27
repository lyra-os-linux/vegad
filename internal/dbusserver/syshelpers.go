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
//
// nmcli runs with LC_ALL=C.UTF-8. The daemon and the query worker have no
// locale (or LC_ALL=C), and there nmcli prints every non-ASCII character as
// "?": the default wired connection of a pt_BR install became
// "Conex?o cabeada 1", and passing that name back (SetStaticIPv4) failed with
// "no such connection profile". C.UTF-8 keeps the English messages; nmcli's
// terse output has no column alignment to break.
func systemCommand(name string, args ...string) *exec.Cmd {
	if name == "zypper" {
		wrapped := append([]string{"-c", `umask 0022; exec /usr/bin/zypper "$@"`, "vega-zypper"}, args...)
		return exec.Command("/bin/sh", wrapped...)
	}
	cmd := exec.Command(name, args...)
	if name == "nmcli" {
		cmd.Env = append(os.Environ(), "LC_ALL=C.UTF-8")
	}
	return cmd
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
