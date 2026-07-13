package distro

// debianProvider bundles Debian/Ubuntu's package/kernel/hardware backends
// (apt/dpkg, update-initramfs+update-grub, ubuntu-drivers) behind the
// Provider interface. Like openSUSE, there's no AUR-equivalent community
// layer, so Community() returns nil — PPAs are out of scope for this pass.
type debianProvider struct {
	pkg  *aptBackend
	kern *debianKernelBackend
	hw   *debianHardwareBackend
}

func newDebianProvider() *debianProvider {
	return &debianProvider{
		pkg:  newAptBackend(),
		kern: newDebianKernelBackend(),
		hw:   newDebianHardwareBackend(),
	}
}

func (d *debianProvider) Distro() ID                  { return Debian }
func (d *debianProvider) Package() PackageBackend     { return d.pkg }
func (d *debianProvider) Community() CommunityBackend { return nil }
func (d *debianProvider) Kernel() KernelBackend       { return d.kern }
func (d *debianProvider) Hardware() HardwareBackend   { return d.hw }

// AdminGroup is "sudo" on Debian/Ubuntu, not "wheel" like Arch/openSUSE.
func (d *debianProvider) AdminGroup() string { return "sudo" }
