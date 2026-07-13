package distro

// fedoraProvider bundles Fedora's package/kernel/hardware backends (DNF,
// dracut+GRUB2, NVIDIA via RPM Fusion) behind the Provider interface.
// Fedora has no AUR-equivalent official community layer built into dnf
// itself (COPR exists but isn't wired up here, same "out of scope for this
// pass" call arch.go's aur.go made necessary before it existed), so
// Community() returns nil like openSUSEProvider.
type fedoraProvider struct {
	pkg  *dnfBackend
	kern *fedoraKernelBackend
	hw   *fedoraHardwareBackend
}

func newFedoraProvider() *fedoraProvider {
	return &fedoraProvider{
		pkg:  newDnfBackend(),
		kern: newFedoraKernelBackend(),
		hw:   newFedoraHardwareBackend(),
	}
}

func (f *fedoraProvider) Distro() ID                  { return Fedora }
func (f *fedoraProvider) Package() PackageBackend     { return f.pkg }
func (f *fedoraProvider) Community() CommunityBackend { return nil }
func (f *fedoraProvider) Kernel() KernelBackend       { return f.kern }
func (f *fedoraProvider) Hardware() HardwareBackend   { return f.hw }
func (f *fedoraProvider) AdminGroup() string          { return "wheel" }
