package distro

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Mesa Vulkan driver per integrated GPU vendor. Without the integrated GPU's
// ICD, GTK 4's Vulkan renderer selects the NVIDIA GPU for every application,
// which keeps it out of runtime D3 on hybrid notebooks (vegad#78).
var integratedVulkanDrivers = map[string]string{
	"0x8086": "libvulkan_intel",
	"0x1002": "libvulkan_radeon",
}

// InstallIntegratedVulkanDrivers runs after the NVIDIA transaction, through
// the system repositories: the private NVIDIA transaction only sees Leap OSS,
// whose Mesa build stops matching the installed one after a Mesa update.
func InstallIntegratedVulkanDrivers() ([]string, error) {
	return installIntegratedVulkanDriversWith("/sys/bus/pci/devices", func(name string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		base := packageCommand(name, args...)
		cmd := exec.CommandContext(ctx, base.Path, base.Args[1:]...)
		cmd.Env = commandEnvC()
		cmd.WaitDelay = 5 * time.Second
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	})
}

func installIntegratedVulkanDriversWith(pciRoot string, output func(string, ...string) (string, error)) ([]string, error) {
	wanted, err := integratedVulkanPackages(pciRoot)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, name := range wanted {
		if _, err := output("rpm", "-q", "--", name); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	args := append([]string{"--non-interactive", "install", "--no-recommends", "--"}, missing...)
	if out, err := output("zypper", args...); err != nil {
		return nil, fmt.Errorf("zypper install %s: %w — %s", strings.Join(missing, " "), err, out)
	}
	return missing, nil
}

// Display-class PCI devices (class 0x03xxxx) from a known integrated GPU vendor.
func integratedVulkanPackages(pciRoot string) ([]string, error) {
	devices, err := filepath.Glob(filepath.Join(pciRoot, "*"))
	if err != nil {
		return nil, err
	}
	var packages []string
	for _, device := range devices {
		class, err := os.ReadFile(filepath.Join(device, "class"))
		if err != nil || !strings.HasPrefix(strings.TrimSpace(string(class)), "0x03") {
			continue
		}
		vendor, err := os.ReadFile(filepath.Join(device, "vendor"))
		if err != nil {
			continue
		}
		name, ok := integratedVulkanDrivers[strings.TrimSpace(string(vendor))]
		if ok && !slices.Contains(packages, name) {
			packages = append(packages, name)
		}
	}
	slices.Sort(packages)
	return packages, nil
}
