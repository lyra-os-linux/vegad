package distro

// archProvider bundles Arch's package/community/kernel/hardware backends
// (Pacman, AUR, mkinitcpio+GRUB, NVIDIA DKMS) behind the Provider interface.
type archProvider struct {
	pkg  *pacmanBackend
	aur  *aurBackend
	kern *archKernelBackend
	hw   *archHardwareBackend
}

func newArchProvider() *archProvider {
	return &archProvider{
		pkg:  newPacmanBackend(),
		aur:  newAurBackend(),
		kern: newArchKernelBackend(),
		hw:   newArchHardwareBackend(),
	}
}

func (a *archProvider) Distro() ID                  { return Arch }
func (a *archProvider) Package() PackageBackend     { return a.pkg }
func (a *archProvider) Community() CommunityBackend { return a.aur }
func (a *archProvider) Kernel() KernelBackend       { return a.kern }
func (a *archProvider) Hardware() HardwareBackend   { return a.hw }
func (a *archProvider) AdminGroup() string          { return "wheel" }
