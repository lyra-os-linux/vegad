// Package distro isolates every Arch/openSUSE-specific detail (package
// manager, kernel packaging, initramfs/bootloader regen, GPU driver
// packages) behind a small set of interfaces, so the rest of vegad
// (dbusserver) never shells out to pacman/zypper/mkinitcpio/dracut
// directly — it asks the active Provider instead.
package distro

// ProgressFunc reports coarse (stage-based, not byte-accurate) progress for
// a running package transaction.
type ProgressFunc func(percent uint32, message string)

// PackageRef identifies a package within one origin ("official", "flathub",
// "aur") so the UI can dedupe the same app found across origins.
type PackageRef struct {
	Origin      string
	Id          string
	Name        string
	Description string
	Installed   bool
	Icon        string
}

// PackageDetails is the expanded view of a single package shown in the
// detail panel — unlike PackageRef, fetching this touches the network/AUR
// helper, so it's only requested on demand, never as part of a list.
type PackageDetails struct {
	Origin           string
	Id               string
	Name             string
	Description      string
	Installed        bool
	InstalledVersion string
	AvailableVersion string
	DownloadSize     string
	InstalledSize    string
	Dependencies     []string
	Licenses         []string
	URL              string
	Maintainer       string
}
