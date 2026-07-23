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
	"github.com/lyraos/vegad/internal/distro"
)

// HardwareService backs org.lyraos.Vega1.Hardware: inventory, NVIDIA
// driver switching (via distro.HardwareBackend) and
// fwupd/LVFS firmware status.
type HardwareService struct {
	activity *Activity
	conn     *dbus.Conn
	provider distro.Provider
}

type HardwareInventory struct {
	CPU     string
	GPU     string
	RAMText string
}

func (h *HardwareService) Inventory() (HardwareInventory, *dbus.Error) {
	h.activity.Touch()
	return HardwareInventory{
		CPU:     cpuModelName(),
		GPU:     gpuDescription(),
		RAMText: ramDescription(),
	}, nil
}

// SwitchNvidiaDriver accepts whatever distro.HardwareBackend.AvailableNvidiaDrivers
// reports for the active distro (e.g. "nvidia-open-dkms"/"nvidia-580xx-dkms"/
// "nouveau") — validity for the detected GPU generation is enforced before
// this is called.
func (h *HardwareService) SwitchNvidiaDriver(sender dbus.Sender, driver string) *dbus.Error {
	h.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.hardware.switch-driver"); err != nil {
		return err
	}

	hw := h.provider.Hardware()
	valid := false
	for _, candidate := range hw.AvailableNvidiaDrivers() {
		if candidate == driver {
			valid = true
			break
		}
	}
	if !valid {
		return dbus.MakeFailedError(fmt.Errorf("driver NVIDIA inválido: %s", driver))
	}

	if err := withSnapshots("Troca de driver NVIDIA: "+driver, func() error {
		return hw.SwitchNvidiaDriver(driver, func(uint32, string) {})
	}); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (h *HardwareService) FirmwareStatus() (string, *dbus.Error) {
	h.activity.Touch()
	if !commandAvailable("fwupdmgr") {
		return "fwupd não disponível neste sistema", nil
	}

	out, err := runCommandOutput("fwupdmgr", "get-updates")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
			// fwupdmgr uses exit code 2 for "nothing to do" (no updates
			// available), not a real failure — the message text itself is
			// locale-dependent so it can't be matched reliably.
			return "Nenhuma atualização de firmware disponível", nil
		}
		return "", dbus.MakeFailedError(fmt.Errorf("fwupdmgr get-updates: %w — %s", err, out))
	}

	if out == "" {
		return "Nenhuma atualização de firmware disponível", nil
	}
	return out, nil
}

func cpuModelName() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "CPU indisponível"
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "model name") || strings.HasPrefix(line, "Hardware") {
			if idx := strings.Index(line, ":"); idx >= 0 {
				return normalizeWhitespace(strings.TrimSpace(line[idx+1:]))
			}
		}
	}
	return "CPU indisponível"
}

func ramDescription() string {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return "RAM indisponível"
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
	return "RAM indisponível"
}

func gpuDescription() string {
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
	return "GPU indisponível"
}
