package distro

import (
	"fmt"
	"strings"
)

// openSUSEKernelBackend drives openSUSE Leap's kernel package and the
// dracut/GRUB2 regeneration a kernel change requires. Leap's desktop
// install ships a single flavor ("kernel-default") rather than Arch's
// linux/linux-lts/linux-zen split, but the mechanism generalizes if that
// ever changes.
type openSUSEKernelBackend struct{}

func newOpenSUSEKernelBackend() *openSUSEKernelBackend { return &openSUSEKernelBackend{} }

var openSUSEKernelPackages = []string{"kernel-default"}

func (k *openSUSEKernelBackend) ListInstalled() ([]string, error) {
	installed, err := zypperInstalledSet()
	if err != nil {
		return nil, err
	}
	var kernels []string
	for _, kernel := range openSUSEKernelPackages {
		if installed[kernel] {
			kernels = append(kernels, kernel)
		}
	}
	return kernels, nil
}

func (k *openSUSEKernelBackend) AvailablePackages() []string {
	return openSUSEKernelPackages
}

func (k *openSUSEKernelBackend) IsSupportedPackage(name string) bool {
	for _, kernel := range openSUSEKernelPackages {
		if kernel == name {
			return true
		}
	}
	return false
}

// RunningKernelMatches checks the flavor suffix `uname -r` carries
// ("...-default", "...-pae", ...), matching openSUSE's kernel-<flavor>
// package naming.
func (k *openSUSEKernelBackend) RunningKernelMatches(name string) bool {
	out, err := runCommandOutput("uname", "-r")
	if err != nil {
		return false
	}
	flavor := strings.TrimPrefix(name, "kernel-")
	return strings.HasSuffix(out, "-"+flavor)
}

func (k *openSUSEKernelBackend) Install(name string, report ProgressFunc) error {
	return runStreamingCommand("zypper", []string{"--non-interactive", "install", "-y", "--", name}, report,
		"Iniciando instalação...", "Instalação concluída")
}

func (k *openSUSEKernelBackend) Remove(name string) error {
	out, err := runCommandOutput("zypper", "--non-interactive", "remove", "-y", "--", name)
	if err != nil {
		return fmt.Errorf("zypper remove %s: %w — %s", name, err, out)
	}
	return nil
}

const openSUSEGrubConfigPath = "/boot/grub2/grub.cfg"

func rebuildOpenSUSEBootArtifacts() error {
	if commandAvailable("dracut") {
		if err := runCommand("dracut", "--regenerate-all", "--force"); err != nil {
			return err
		}
	}
	if commandAvailable("grub2-mkconfig") {
		if err := runCommand("grub2-mkconfig", "-o", openSUSEGrubConfigPath); err != nil {
			return err
		}
	}
	return nil
}

func (k *openSUSEKernelBackend) RebuildBootArtifacts() error { return rebuildOpenSUSEBootArtifacts() }
func (k *openSUSEKernelBackend) GrubConfigPath() string      { return openSUSEGrubConfigPath }
