package distro

import "fmt"

// openSUSEHardwareBackend drives openSUSE Leap's NVIDIA driver packages,
// pulled from NVIDIA's official openSUSE repo (download.nvidia.com/opensuse).
// Package names follow NVIDIA's "G06" (Turing and newer) generation family
// — GPU-generation compatibility is assumed already validated by the
// caller, same as the Arch side. Unlike pacmanBackend/aurBackend, this has
// not been exercised against a live Leap install with the NVIDIA repo
// enabled; treat the package names as a documented starting point to
// confirm, not a guarantee.
type openSUSEHardwareBackend struct{}

func newOpenSUSEHardwareBackend() *openSUSEHardwareBackend { return &openSUSEHardwareBackend{} }

func (h *openSUSEHardwareBackend) AvailableNvidiaDrivers() []string {
	return []string{"nvidia-open-driver-G06-signed-kmp-default", "nvidia-driver-G06-kmp-default", "nouveau"}
}

var openSUSENvidiaPackages = []string{
	"nvidia-open-driver-G06-signed-kmp-default",
	"nvidia-driver-G06-kmp-default",
	"nvidia-video-G06",
	"nvidia-compute-G06",
}

func (h *openSUSEHardwareBackend) SwitchNvidiaDriver(driver string, report ProgressFunc) error {
	installed, err := zypperInstalledSet()
	if err != nil {
		return err
	}

	var removePackages, installPackages []string
	switch driver {
	case "nouveau":
		for _, pkg := range openSUSENvidiaPackages {
			if installed[pkg] {
				removePackages = append(removePackages, pkg)
			}
		}
	default:
		for _, pkg := range openSUSENvidiaPackages {
			if pkg != driver && installed[pkg] {
				removePackages = append(removePackages, pkg)
			}
		}
		installPackages = append(installPackages, driver, "nvidia-video-G06")
	}

	for _, pkg := range removePackages {
		out, err := runCommandOutput("zypper", "--non-interactive", "remove", "-y", "--", pkg)
		if err != nil {
			return fmt.Errorf("zypper remove %s: %w — %s", pkg, err, out)
		}
	}
	for _, pkg := range installPackages {
		if installed[pkg] {
			continue
		}
		if err := runStreamingCommand("zypper", []string{"--non-interactive", "install", "-y", "--", pkg}, report,
			"Instalando "+pkg+"...", "Concluído"); err != nil {
			return err
		}
	}

	return rebuildOpenSUSEBootArtifacts()
}
