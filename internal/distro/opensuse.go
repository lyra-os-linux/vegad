package distro

// openSUSEProvider bundles openSUSE Leap's package/kernel backends
// (Zypper and dracut+GRUB2) behind the Provider interface.
type openSUSEProvider struct {
	pkg  *zypperBackend
	kern *openSUSEKernelBackend
}

func newOpenSUSEProvider() *openSUSEProvider {
	return &openSUSEProvider{
		pkg:  newZypperBackend(),
		kern: newOpenSUSEKernelBackend(),
	}
}

func (o *openSUSEProvider) Distro() ID              { return OpenSUSELeap }
func (o *openSUSEProvider) Package() PackageBackend { return o.pkg }
func (o *openSUSEProvider) Kernel() KernelBackend   { return o.kern }
func (o *openSUSEProvider) AdminGroup() string      { return "wheel" }
