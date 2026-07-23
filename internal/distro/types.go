// Package distro isolates every openSUSE-specific detail (package manager,
// kernel packaging, initramfs/bootloader regen, GPU driver packages) behind
// a small set of interfaces, so the rest of vegad (dbusserver) never shells
// out to zypper/dracut directly — it asks the active Provider instead.
package distro

import (
	"fmt"
	"strings"
)

// ProgressFunc reports coarse (stage-based, not byte-accurate) progress for
// a running package transaction.
type ProgressFunc func(percent uint32, message string)

// RepositoryRef identifies a configured package repository and its current
// state as reported by the distribution package manager.
type RepositoryRef struct {
	Name    string
	Enabled bool
}

// UntrustedKeyError is returned by AddRepo (and a retried TrustRepoKey) when
// the repository's signing key was found but isn't trusted yet — the caller
// (dbusserver) turns this into a RepoKeyPending signal instead of a plain
// failure, so the UI can show the fingerprint/userId and let the user
// approve importing it via TrustRepoKey. This is trust-on-first-use, the
// same level of verification pacman/zypper/dnf already give when a human
// approves the equivalent prompt at the terminal — not a stronger guarantee
// that the key actually belongs to whoever publishes the repo.
type UntrustedKeyError struct {
	Repo        string
	KeyId       string
	Fingerprint string
	UserId      string
}

func (e *UntrustedKeyError) Error() string {
	return fmt.Sprintf("repositório %q assinado por chave não confiada %s (%s)", e.Repo, e.KeyId, e.UserId)
}

// SplitPackageList splits a space-separated zypper-style list field
// ("Licenses", "Depends On", ...) into its entries, treating zypper's own
// "None" as empty. Shared with flatpak.go's license parsing since both
// formats use the same convention.
func SplitPackageList(value string) []string {
	if value == "" || value == "None" {
		return nil
	}
	return strings.Fields(value)
}

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
