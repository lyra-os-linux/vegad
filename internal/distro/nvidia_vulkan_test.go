package distro

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePCIDevice(t *testing.T, root, slot, class, vendor string) {
	t.Helper()
	dir := filepath.Join(root, slot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"class": class + "\n", "vendor": vendor + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// Hybrid notebook from vegad#78: Intel iGPU, NVIDIA dGPU and an Intel
// non-display device that must not count as a GPU.
func TestIntegratedVulkanDriversInstallsOnlyMissingIGPUDriver(t *testing.T) {
	root := t.TempDir()
	writePCIDevice(t, root, "0000:00:02.0", "0x030000", "0x8086")
	writePCIDevice(t, root, "0000:01:00.0", "0x030200", "0x10de")
	writePCIDevice(t, root, "0000:00:14.0", "0x0c0330", "0x8086")
	var calls []string
	installed, err := installIntegratedVulkanDriversWith(root, func(name string, args ...string) (string, error) {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		if name == "rpm" {
			return "package libvulkan_intel is not installed", errors.New("exit status 1")
		}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"rpm -q -- libvulkan_intel", "zypper --non-interactive install --no-recommends -- libvulkan_intel"}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") || strings.Join(installed, " ") != "libvulkan_intel" {
		t.Fatalf("calls = %q, installed = %q", calls, installed)
	}
}

func TestIntegratedVulkanDriversSkipsInstalledAndUnknownVendors(t *testing.T) {
	root := t.TempDir()
	writePCIDevice(t, root, "0000:00:02.0", "0x030000", "0x8086")
	writePCIDevice(t, root, "0000:05:00.0", "0x030000", "0x1002")
	writePCIDevice(t, root, "0000:06:00.0", "0x030000", "0x1a03")
	installed, err := installIntegratedVulkanDriversWith(root, func(name string, args ...string) (string, error) {
		if name != "rpm" {
			t.Fatalf("unexpected command %s %q", name, args)
		}
		return "installed", nil
	})
	if err != nil || installed != nil {
		t.Fatalf("installed = %q, err = %v", installed, err)
	}
	packages, _ := integratedVulkanPackages(root)
	if strings.Join(packages, " ") != "libvulkan_intel libvulkan_radeon" {
		t.Fatalf("packages = %q", packages)
	}
}

func TestIntegratedVulkanDriversReportsZypperFailure(t *testing.T) {
	root := t.TempDir()
	writePCIDevice(t, root, "0000:00:02.0", "0x030000", "0x8086")
	_, err := installIntegratedVulkanDriversWith(root, func(name string, args ...string) (string, error) {
		if name == "rpm" {
			return "", errors.New("exit status 1")
		}
		return "Problem: nothing provides libvulkan_intel", errors.New("exit status 4")
	})
	if err == nil || !strings.Contains(err.Error(), "nothing provides") {
		t.Fatalf("err = %v", err)
	}
}
