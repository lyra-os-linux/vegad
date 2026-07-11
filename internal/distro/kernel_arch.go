package distro

import (
	"fmt"
	"strings"
)

// archKernelBackend drives Arch's kernel packages (linux, linux-lts,
// linux-zen) and the mkinitcpio/GRUB regeneration a kernel change requires.
type archKernelBackend struct{}

func newArchKernelBackend() *archKernelBackend { return &archKernelBackend{} }

var archKernelPackages = []string{"linux", "linux-lts", "linux-zen"}

func (k *archKernelBackend) ListInstalled() ([]string, error) {
	installed, err := pacmanInstalledSet()
	if err != nil {
		return nil, err
	}
	var kernels []string
	for _, kernel := range archKernelPackages {
		if installed[kernel] {
			kernels = append(kernels, kernel)
		}
	}
	return kernels, nil
}

func (k *archKernelBackend) AvailablePackages() []string {
	return archKernelPackages
}

func (k *archKernelBackend) IsSupportedPackage(name string) bool {
	for _, kernel := range archKernelPackages {
		if kernel == name {
			return true
		}
	}
	return false
}

// RunningKernelMatches infers the running flavor from `uname -r`, which
// carries "-zen" or "-lts" for those flavors and neither for stock "linux".
func (k *archKernelBackend) RunningKernelMatches(name string) bool {
	out, err := runCommandOutput("uname", "-r")
	if err != nil {
		return false
	}
	switch name {
	case "linux-zen":
		return strings.Contains(out, "zen")
	case "linux-lts":
		return strings.Contains(out, "lts")
	default:
		return !strings.Contains(out, "zen") && !strings.Contains(out, "lts")
	}
}

func (k *archKernelBackend) Install(name string, report ProgressFunc) error {
	return runPacmanTransaction([]string{"-S", "--noconfirm", "--", name}, report)
}

func (k *archKernelBackend) Remove(name string) error {
	out, err := runCommandOutput("pacman", "-R", "--noconfirm", "--", name)
	if err != nil {
		return fmt.Errorf("pacman -R %s: %w — %s", name, err, out)
	}
	return nil
}

const archGrubConfigPath = "/boot/grub/grub.cfg"

func rebuildArchBootArtifacts() error {
	if commandAvailable("mkinitcpio") {
		if err := runCommand("mkinitcpio", "-P"); err != nil {
			return err
		}
	}
	if commandAvailable("grub-mkconfig") {
		if err := runCommand("grub-mkconfig", "-o", archGrubConfigPath); err != nil {
			return err
		}
	}
	return nil
}

func (k *archKernelBackend) RebuildBootArtifacts() error { return rebuildArchBootArtifacts() }
func (k *archKernelBackend) GrubConfigPath() string      { return archGrubConfigPath }
