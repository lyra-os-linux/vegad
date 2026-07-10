package dbusserver

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/version"
)

// SystemService backs org.lyraos.Vega1.System — the minimal interface the
// UI uses to confirm vegad is reachable before touching any privileged
// module.
type SystemService struct {
	activity *Activity
}

func (s *SystemService) Version() (string, *dbus.Error) {
	s.activity.Touch()
	return version.Version, nil
}

func (s *SystemService) Ping() (bool, *dbus.Error) {
	s.activity.Touch()
	return true, nil
}

// DiskUsage reports used/total space (human-readable, e.g. "126G"/"476G")
// and percent used for the root filesystem — a minimal stat for the
// dashboard (issue #17), not the full disk/partition inventory of the
// future storage module (issue #16).
func (s *SystemService) DiskUsage() (string, string, uint32, *dbus.Error) {
	s.activity.Touch()

	out, err := runCommandOutput("df", "--output=used,size,pcent", "-h", "/")
	if err != nil {
		return "", "", 0, dbus.MakeFailedError(fmt.Errorf("df: %w — %s", err, out))
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return "", "", 0, dbus.MakeFailedError(fmt.Errorf("df: saída inesperada: %q", out))
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 3 {
		return "", "", 0, dbus.MakeFailedError(fmt.Errorf("df: saída inesperada: %q", out))
	}

	used, total := fields[0], fields[1]
	percent, _ := strconv.ParseUint(strings.TrimSuffix(fields[2], "%"), 10, 32)
	return used, total, uint32(percent), nil
}
