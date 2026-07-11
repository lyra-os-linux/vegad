package distro

// openSUSEProvider bundles openSUSE Leap's package/kernel/hardware backends
// (Zypper, dracut+GRUB2, NVIDIA G06) behind the Provider interface. Leap has
// no AUR-equivalent community layer, so Community() returns nil.
type openSUSEProvider struct {
	pkg  *zypperBackend
	kern *openSUSEKernelBackend
	hw   *openSUSEHardwareBackend
}

func newOpenSUSEProvider() *openSUSEProvider {
	return &openSUSEProvider{
		pkg:  newZypperBackend(),
		kern: newOpenSUSEKernelBackend(),
		hw:   newOpenSUSEHardwareBackend(),
	}
}

func (o *openSUSEProvider) Distro() ID                  { return OpenSUSELeap }
func (o *openSUSEProvider) Package() PackageBackend     { return o.pkg }
func (o *openSUSEProvider) Community() CommunityBackend { return nil }
func (o *openSUSEProvider) Kernel() KernelBackend       { return o.kern }
func (o *openSUSEProvider) Hardware() HardwareBackend   { return o.hw }
func (o *openSUSEProvider) AdminGroup() string          { return "wheel" }
