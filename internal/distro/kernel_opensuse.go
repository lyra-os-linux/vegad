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
	cmd := packageCommand("zypper", "--non-interactive", "install", "-y", "--", name)
	cmd.Env = commandEnvC()
	return explainZypperStreamError(runStreamingCmd(cmd, report, "Iniciando instalação...", "Instalação concluída"))
}

func (k *openSUSEKernelBackend) Remove(name string) error {
	out, err := zypperOutputC("--non-interactive", "remove", "-y", "--", name)
	if err != nil {
		return explainZypperError(fmt.Errorf("zypper remove %s: %w — %s", name, err, out), out)
	}
	return nil
}

const openSUSEGrubConfigPath = "/boot/grub2/grub.cfg"

// rebuildOpenSUSEBootArtifacts regenerates the initrd and the GRUB menu. A
// missing tool is an error, checked before running either: reporting
// success without them would leave a kernel change that may not boot.
func rebuildOpenSUSEBootArtifacts() error {
	for _, tool := range []string{"dracut", "grub2-mkconfig"} {
		if !commandAvailable(tool) {
			return fmt.Errorf("%s não encontrado: o initrd e o menu de boot não foram atualizados", tool)
		}
	}
	if err := runCommand("dracut", "--regenerate-all", "--force"); err != nil {
		return err
	}
	return runCommand("grub2-mkconfig", "-o", openSUSEGrubConfigPath)
}

func (k *openSUSEKernelBackend) RebuildBootArtifacts() error { return rebuildOpenSUSEBootArtifacts() }
func (k *openSUSEKernelBackend) GrubConfigPath() string      { return openSUSEGrubConfigPath }
