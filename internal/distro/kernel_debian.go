package distro

import (
	"fmt"
	"regexp"
	"strings"
)

// debianKernelBackend drives Debian/Ubuntu's linux-image packages and the
// update-initramfs/update-grub regeneration a kernel change requires.
type debianKernelBackend struct{}

func newDebianKernelBackend() *debianKernelBackend { return &debianKernelBackend{} }

// debianKernelMetaPackages are the installable meta-packages this backend
// offers in the UI's "install a kernel" picker — always resolving to
// whatever the latest kernel in the configured repos is, since Debian/
// Ubuntu don't support pinning an exact version through a stable package
// name the way Arch's linux/linux-lts/linux-zen split does. Exact HWE/
// variant meta-package names shift across Ubuntu releases; treat this as a
// documented starting point to confirm on a live install, not a guarantee,
// same caveat hardware_opensuse.go carries for its NVIDIA package names.
var debianKernelMetaPackages = []string{"linux-image-generic", "linux-image-lowlatency"}

// debianVersionedKernelImage matches a concrete, already-installed kernel
// image package (e.g. "linux-image-6.8.0-31-generic"), as opposed to a
// meta-package that just tracks "whatever is latest".
var debianVersionedKernelImage = regexp.MustCompile(`^linux-image-[0-9]`)

func (k *debianKernelBackend) ListInstalled() ([]string, error) {
	installed, err := aptInstalledSet()
	if err != nil {
		return nil, err
	}
	var kernels []string
	for name := range installed {
		if debianVersionedKernelImage.MatchString(name) {
			kernels = append(kernels, name)
		}
	}
	return kernels, nil
}

func (k *debianKernelBackend) AvailablePackages() []string {
	return debianKernelMetaPackages
}

func (k *debianKernelBackend) IsSupportedPackage(name string) bool {
	if debianVersionedKernelImage.MatchString(name) {
		return true
	}
	for _, pkg := range debianKernelMetaPackages {
		if pkg == name {
			return true
		}
	}
	return false
}

// RunningKernelMatches checks whether name's version suffix matches `uname
// -r` — Debian/Ubuntu kernel image package names embed the exact running
// version (e.g. "linux-image-6.8.0-31-generic" for `uname -r` "6.8.0-31-generic"),
// unlike openSUSE's flavor-suffix-only naming.
func (k *debianKernelBackend) RunningKernelMatches(name string) bool {
	out, err := runCommandOutput("uname", "-r")
	if err != nil {
		return false
	}
	return name == "linux-image-"+out
}

func (k *debianKernelBackend) Install(name string, report ProgressFunc) error {
	return runAptGet([]string{"install", "-y", "--", name}, report,
		"Iniciando instalação...", "Instalação concluída")
}

func (k *debianKernelBackend) Remove(name string) error {
	cmd := aptGetCommand("remove", "-y", "--", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("apt-get remove %s: %w — %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

const debianGrubConfigPath = "/boot/grub/grub.cfg"

func rebuildDebianBootArtifacts() error {
	if commandAvailable("update-initramfs") {
		if err := runCommand("update-initramfs", "-u", "-k", "all"); err != nil {
			return err
		}
	}
	if commandAvailable("update-grub") {
		if err := runCommand("update-grub"); err != nil {
			return err
		}
	}
	return nil
}

func (k *debianKernelBackend) RebuildBootArtifacts() error { return rebuildDebianBootArtifacts() }
func (k *debianKernelBackend) GrubConfigPath() string      { return debianGrubConfigPath }
