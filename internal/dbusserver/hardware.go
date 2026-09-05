package dbusserver

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
	vegai18n "github.com/lyraos/vegad/internal/i18n"
)

// HardwareService backs org.lyraos.Vega1.Hardware inventory and
// fwupd/LVFS firmware status.
type HardwareService struct {
	activity *Activity
}

type HardwareInventory struct {
	CPU     string
	GPU     string
	RAMText string
}

func (h *HardwareService) Inventory() (HardwareInventory, *dbus.Error) {
	return h.InventoryLocalized(vegai18n.DefaultLocale)
}

func (h *HardwareService) InventoryLocalized(locale string) (HardwareInventory, *dbus.Error) {
	h.activity.Touch()
	return HardwareInventory{
		CPU:     cpuModelName(locale),
		GPU:     gpuDescription(locale),
		RAMText: ramDescription(locale),
	}, nil
}

// SwitchNvidiaDriver keeps the v1 wire signature for older clients, but
// driver switching is no longer a Vega operation.
func (h *HardwareService) SwitchNvidiaDriver(_ dbus.Sender, _ string) *dbus.Error {
	return driverManagementRemoved()
}

func (h *HardwareService) FirmwareStatus() (string, *dbus.Error) {
	return h.FirmwareStatusLocalized(vegai18n.DefaultLocale)
}

func (h *HardwareService) FirmwareStatusLocalized(locale string) (string, *dbus.Error) {
	h.activity.Touch()
	if !commandAvailable("fwupdmgr") {
		return vegai18n.T(locale, "hardware.fwupd_unavailable"), nil
	}

	out, err := runCommandOutput("fwupdmgr", "get-updates")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
			// fwupdmgr uses exit code 2 for "nothing to do" (no updates
			// available), not a real failure — the message text itself is
			// locale-dependent so it can't be matched reliably.
			return vegai18n.T(locale, "hardware.no_firmware_updates"), nil
		}
		return "", dbus.MakeFailedError(fmt.Errorf("fwupdmgr get-updates: %w — %s", err, out))
	}

	if out == "" {
		return vegai18n.T(locale, "hardware.no_firmware_updates"), nil
	}
	return out, nil
}

func cpuModelName(locale string) string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return vegai18n.T(locale, "hardware.cpu_unavailable")
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "model name") || strings.HasPrefix(line, "Hardware") {
			if idx := strings.Index(line, ":"); idx >= 0 {
				return normalizeWhitespace(strings.TrimSpace(line[idx+1:]))
			}
		}
	}
	return vegai18n.T(locale, "hardware.cpu_unavailable")
}

func ramDescription(locale string) string {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return vegai18n.T(locale, "hardware.ram_unavailable")
	}
	re := regexp.MustCompile(`^MemTotal:\s+(\d+)\s+kB$`)
	for _, line := range strings.Split(string(data), "\n") {
		if m := re.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			kb, err := strconv.ParseFloat(m[1], 64)
			if err != nil {
				break
			}
			gb := kb / 1024 / 1024
			return fmt.Sprintf("%.1f GiB", gb)
		}
	}
	return vegai18n.T(locale, "hardware.ram_unavailable")
}

func gpuDescription(locale string) string {
	if commandAvailable("lspci") {
		out, err := runCommandOutput("lspci", "-nn")
		if err == nil {
			for _, line := range strings.Split(out, "\n") {
				line = strings.TrimSpace(line)
				if strings.Contains(line, "VGA compatible controller") ||
					strings.Contains(line, "3D controller") ||
					strings.Contains(line, "Display controller") {
					return normalizeWhitespace(line)
				}
			}
		}
	}
	return vegai18n.T(locale, "hardware.gpu_unavailable")
}
