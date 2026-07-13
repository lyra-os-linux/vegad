package distro

import "strings"

// fedoraKernelBackend drives Fedora's kernel package and the dracut/GRUB2
// regeneration a kernel change requires. Unlike openSUSE Leap's single
// "kernel-default" flavor, Fedora's "kernel" meta-package is designed to
// have several versions installed side by side (installonly), so
// RunningKernelMatches checks the running `uname -r` against every
// installed "kernel" version rather than assuming just one is present.
type fedoraKernelBackend struct{}

func newFedoraKernelBackend() *fedoraKernelBackend { return &fedoraKernelBackend{} }

var fedoraKernelPackages = []string{"kernel"}

func (k *fedoraKernelBackend) ListInstalled() ([]string, error) {
	installed, err := dnfInstalledSet()
	if err != nil {
		return nil, err
	}
	var kernels []string
	for _, kernel := range fedoraKernelPackages {
		if installed[kernel] {
			kernels = append(kernels, kernel)
		}
	}
	return kernels, nil
}

func (k *fedoraKernelBackend) AvailablePackages() []string {
	return fedoraKernelPackages
}

func (k *fedoraKernelBackend) IsSupportedPackage(name string) bool {
	for _, kernel := range fedoraKernelPackages {
		if kernel == name {
			return true
		}
	}
	return false
}

// RunningKernelMatches compares `uname -r` against every installed "kernel"
// version-release (rpm -q returns one line per coexisting kernel version),
// since Fedora keeps old kernels installed by default rather than
// replacing them in place like Arch/openSUSE.
func (k *fedoraKernelBackend) RunningKernelMatches(name string) bool {
	if name != "kernel" {
		return false
	}
	running, err := runCommandOutput("uname", "-r")
	if err != nil {
		return false
	}
	out, err := runCommandOutput("rpm", "-q", "kernel", "--qf", "%{VERSION}-%{RELEASE}.%{ARCH}\n")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == running {
			return true
		}
	}
	return false
}

func (k *fedoraKernelBackend) Install(name string, report ProgressFunc) error {
	return runStreamingCommand("dnf", []string{"-y", "install", "--", name}, report,
		"Iniciando instalação...", "Instalação concluída")
}

func (k *fedoraKernelBackend) Remove(name string) error {
	return runCommand("dnf", "-y", "remove", "--", name)
}

// fedoraGrubConfigPath assumes a BIOS/legacy layout. Fedora 38+ defaults to
// BLS (Boot Loader Specification) entries managed by `grubby` rather than a
// single regenerated grub.cfg on UEFI installs — this backend hasn't been
// validated against that setup (no Fedora machine available while writing
// it), so RebuildBootArtifacts sticking to dracut+grub2-mkconfig should be
// treated as a documented starting point, same caveat
// openSUSEHardwareBackend already carries for its NVIDIA packages.
const fedoraGrubConfigPath = "/boot/grub2/grub.cfg"

func (k *fedoraKernelBackend) RebuildBootArtifacts() error { return rebuildFedoraBootArtifacts() }
func (k *fedoraKernelBackend) GrubConfigPath() string      { return fedoraGrubConfigPath }
