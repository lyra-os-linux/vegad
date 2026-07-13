package distro

import "fmt"

// PackageBackend drives the distro's primary package manager (Pacman on
// Arch, Zypper on openSUSE Leap).
type PackageBackend interface {
	// Name is the human-facing label for this backend ("Pacman"/"Zypper"),
	// shown in the UI wherever it used to say "Pacman" unconditionally.
	Name() string

	Search(query string) ([]PackageRef, error)
	ListInstalled() ([]PackageRef, error)
	ListUpdates() ([]PackageRef, error)
	// SyncDatabase refreshes local package metadata from the configured
	// repos (`pacman -Sy` / `zypper refresh`). Touches the network and
	// needs root; only the periodic update-check job calls it.
	SyncDatabase() error
	GetDetails(id string) (PackageDetails, error)
	Install(id string, report ProgressFunc) error
	Remove(id string, report ProgressFunc) error
	UpdateAll(report ProgressFunc) error
	ClearCache(report ProgressFunc) error
	ListRepos() ([]string, error)
	SetRepoEnabled(repo string, enabled bool) error

	// OptimizeMirrors re-ranks/refreshes mirrors, where the concept exists
	// (reflector on Arch). Backends without an equivalent (openSUSE, whose
	// download redirector already picks the best mirror) return
	// ErrUnsupported.
	OptimizeMirrors(report ProgressFunc) error
}

// CommunityBackend drives an optional community package layer (AUR via
// yay/paru on Arch). Providers without one (openSUSE Leap today) return nil
// from Provider.Community() — callers must nil-check before use.
type CommunityBackend interface {
	// Name is the human-facing label for this layer ("AUR"), shown in the
	// UI wherever it used to say "AUR" unconditionally.
	Name() string

	Search(query string) ([]PackageRef, error)
	GetDetails(id string) (PackageDetails, error)
	GetBuildScript(id string) (string, error)
	Install(id string, report ProgressFunc) error
}

// KernelBackend knows the distro's kernel package naming and how to
// regenerate initramfs/bootloader artifacts after a kernel change.
type KernelBackend interface {
	ListInstalled() ([]string, error)
	// AvailablePackages lists every kernel package this backend knows how to
	// install for the active distro (not just what's installed) — the UI
	// populates its "install a kernel" picker from this instead of
	// hardcoding Arch package names.
	AvailablePackages() []string
	IsSupportedPackage(name string) bool
	RunningKernelMatches(name string) bool
	Install(name string, report ProgressFunc) error
	Remove(name string) error

	// RebuildBootArtifacts regenerates the initramfs (mkinitcpio/dracut)
	// and bootloader config (grub-mkconfig/grub2-mkconfig) after a kernel
	// or driver change.
	RebuildBootArtifacts() error

	// GrubConfigPath is where grub-mkconfig/grub2-mkconfig writes its
	// generated config — it differs between distros
	// (/boot/grub/grub.cfg vs /boot/grub2/grub.cfg).
	GrubConfigPath() string
}

// HardwareBackend knows the distro-specific package names/steps to switch
// GPU drivers.
type HardwareBackend interface {
	// AvailableNvidiaDrivers lists the driver identifiers this backend
	// accepts, in display order — the UI populates its dropdown from this
	// instead of hardcoding Arch package names.
	AvailableNvidiaDrivers() []string
	SwitchNvidiaDriver(driver string, report ProgressFunc) error
}

// Provider bundles every distro-specific backend for one detected distro.
type Provider interface {
	Distro() ID
	Package() PackageBackend
	// Community returns nil when this distro has no AUR-equivalent layer.
	Community() CommunityBackend
	Kernel() KernelBackend
	Hardware() HardwareBackend
	// AdminGroup is the group membership that grants sudo/wheel access
	// ("wheel" on both distros today).
	AdminGroup() string
}

// NewProvider builds the Provider for a detected distro.
func NewProvider(d ID) (Provider, error) {
	switch d {
	case Arch:
		return newArchProvider(), nil
	case OpenSUSELeap:
		return newOpenSUSEProvider(), nil
	case Debian:
		return newDebianProvider(), nil
	default:
		return nil, fmt.Errorf("distro: nenhum provider disponível para %q", d)
	}
}
