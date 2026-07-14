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

func commandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func runCommandOutput(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
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
