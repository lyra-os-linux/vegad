package distro

// archHardwareBackend drives Arch's NVIDIA driver switching via pacman,
// swapping between the proprietary DKMS driver and the open-source nouveau
// fallback. GPU-generation validity (which driver a given card can run) is
// assumed already checked by the caller before this runs.
type archHardwareBackend struct{}

func newArchHardwareBackend() *archHardwareBackend { return &archHardwareBackend{} }

func (h *archHardwareBackend) AvailableNvidiaDrivers() []string {
	return []string{"nvidia-open-dkms", "nvidia-580xx-dkms", "nouveau"}
}

var archNvidiaPackages = []string{"nvidia-open-dkms", "nvidia-580xx-dkms", "nvidia", "nvidia-utils", "nvidia-settings"}

func (h *archHardwareBackend) SwitchNvidiaDriver(driver string, report ProgressFunc) error {
	installed, err := pacmanInstalledSet()
	if err != nil {
		return err
	}

	var removePackages, installPackages []string
	switch driver {
	case "nouveau":
		for _, pkg := range archNvidiaPackages {
			if installed[pkg] {
				removePackages = append(removePackages, pkg)
			}
		}
		installPackages = append(installPackages, "xf86-video-nouveau")
	default:
		for _, pkg := range archNvidiaPackages {
			if pkg != driver && installed[pkg] {
				removePackages = append(removePackages, pkg)
			}
		}
		installPackages = append(installPackages, driver)
	}

	if len(removePackages) > 0 {
		args := append([]string{"-R", "--noconfirm", "--"}, removePackages...)
		if err := runPacmanTransaction(args, report); err != nil {
			return err
		}
	}

	for _, pkg := range installPackages {
		if installed[pkg] {
			continue
		}
		if err := runPacmanTransaction([]string{"-S", "--noconfirm", "--", pkg}, report); err != nil {
			return err
		}
	}

	return rebuildArchBootArtifacts()
}
