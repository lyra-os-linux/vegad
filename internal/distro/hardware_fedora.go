package distro

import "fmt"

// fedoraHardwareBackend drives Fedora's NVIDIA driver packages, which come
// from RPM Fusion's nonfree repo (rpmfusion-nonfree), not Fedora's own
// repos — Fedora ships no proprietary NVIDIA driver itself. This assumes
// RPM Fusion is already enabled (Vega doesn't auto-add third-party repos,
// same stance as openSUSEHardwareBackend assuming the NVIDIA repo is
// already configured). akmod-nvidia also builds its kernel module
// asynchronously via the akmods systemd service after install, not
// synchronously as part of `dnf install` returning — the caller should
// expect a delay (and possibly a reboot) before the driver is actually
// active, not just before this call returns. Not exercised against a live
// Fedora+RPM Fusion install (no Fedora machine available while writing
// this) — treat as a documented starting point to confirm, same caveat
// openSUSEHardwareBackend already carries.
type fedoraHardwareBackend struct{}

func newFedoraHardwareBackend() *fedoraHardwareBackend { return &fedoraHardwareBackend{} }

func (h *fedoraHardwareBackend) AvailableNvidiaDrivers() []string {
	return []string{"akmod-nvidia", "nouveau"}
}

var fedoraNvidiaPackages = []string{"akmod-nvidia", "xorg-x11-drv-nvidia-cuda"}

func (h *fedoraHardwareBackend) SwitchNvidiaDriver(driver string, report ProgressFunc) error {
	installed, err := dnfInstalledSet()
	if err != nil {
		return err
	}

	var removePackages, installPackages []string
	switch driver {
	case "nouveau":
		// nouveau is the in-tree driver already present via the kernel and
		// xorg-x11-drv-nouveau — nothing to install, just remove the
		// proprietary packages so the system falls back to it.
		for _, pkg := range fedoraNvidiaPackages {
			if installed[pkg] {
				removePackages = append(removePackages, pkg)
			}
		}
	default:
		installPackages = append(installPackages, fedoraNvidiaPackages...)
	}

	for _, pkg := range removePackages {
		out, err := runCommandOutput("dnf", "-y", "remove", "--", pkg)
		if err != nil {
			return fmt.Errorf("dnf remove %s: %w — %s", pkg, err, out)
		}
	}
	for _, pkg := range installPackages {
		if installed[pkg] {
			continue
		}
		if err := runStreamingCommand("dnf", []string{"-y", "install", "--", pkg}, report,
			"Instalando "+pkg+"...", "Concluído"); err != nil {
			return err
		}
	}

	return rebuildFedoraBootArtifacts()
}

// rebuildFedoraBootArtifacts is shared with fedoraKernelBackend.RebuildBootArtifacts
// via the same dracut+grub2-mkconfig pair — a driver swap needs the
// initramfs regenerated just like a kernel change does.
func rebuildFedoraBootArtifacts() error {
	if commandAvailable("dracut") {
		if err := runCommand("dracut", "--regenerate-all", "--force"); err != nil {
			return err
		}
	}
	if commandAvailable("grub2-mkconfig") {
		if err := runCommand("grub2-mkconfig", "-o", fedoraGrubConfigPath); err != nil {
			return err
		}
	}
	return nil
}
