package parental

import (
	"regexp"
	"strings"
)

const UnresolvedFlatpakDeployment Code = "UnresolvedFlatpakDeployment"

var flatpakRefComponent = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var flatpakPublisher = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// FlatpakApproval is a server-owned rule. A publisher fingerprint allows
// updates from the same verified publisher; without one, approval pins the
// exact commit and never carries across updates automatically.
type FlatpakApproval struct {
	Ref, Scope, Installation, Origin, PublisherFingerprint, Commit string
	OwnerUID                                                       uint32
}

// InstalledFlatpak is supplied by a trusted installation inventory, never by
// the launch client. The inventory must attest the publisher and derive the
// active commit/runtime atomically or be rechecked under the launch gate.
type InstalledFlatpak struct {
	Ref, Scope, Installation, Origin, PublisherFingerprint, Commit string
	RuntimeRef, RuntimeCommit                                      string
	OwnerUID                                                       uint32
}

type ResolvedFlatpak struct{ InstalledFlatpak }

func flatpakFullRef(ref, kind string) (string, bool) {
	parts := strings.Split(ref, "/")
	if len(parts) != 4 || parts[0] != kind || !launchAppID.MatchString(parts[1]) ||
		!flatpakRefComponent.MatchString(parts[2]) || !flatpakRefComponent.MatchString(parts[3]) {
		return "", false
	}
	return parts[1], true
}

func validFlatpakScope(scope, installation string, ownerUID, childUID uint32) bool {
	if !flatpakRefComponent.MatchString(installation) {
		return false
	}
	if scope == "system" {
		return ownerUID == 0
	}
	return scope == "user" && ownerUID == childUID && childUID != 0
}

// ResolveFlatpakDeployment selects one installed app for one already bound
// supervised account. It refuses all ambiguous installs, even if only one
// entry matches the approval. It neither reads Flatpak state nor launches it.
func ResolveFlatpakDeployment(request FlatpakLaunchRequest, binding FlatpakPeerBinding,
	approval FlatpakApproval, installed []InstalledFlatpak) (ResolvedFlatpak, error) {
	if len(request.AppID) > 255 || !launchAppID.MatchString(request.AppID) ||
		binding.UID == 0 || binding.GID == 0 || binding.CatalogRevision == 0 ||
		!token.MatchString(binding.Association) || !token.MatchString(binding.Generation) ||
		!hash.MatchString(binding.CatalogDigest) || binding.SELinuxContext == "" || len(installed) > 256 {
		return ResolvedFlatpak{}, UnresolvedFlatpakDeployment
	}
	approvedID, ok := flatpakFullRef(approval.Ref, "app")
	if !ok || approvedID != request.AppID || !validFlatpakScope(approval.Scope, approval.Installation, approval.OwnerUID, binding.UID) ||
		approval.Origin == "" || !flatpakRefComponent.MatchString(approval.Origin) ||
		(approval.PublisherFingerprint != "" && !flatpakPublisher.MatchString(approval.PublisherFingerprint)) ||
		(approval.Commit != "" && !hash.MatchString(approval.Commit)) ||
		(approval.PublisherFingerprint == "" && !hash.MatchString(approval.Commit)) {
		return ResolvedFlatpak{}, UnresolvedFlatpakDeployment
	}
	var selected *InstalledFlatpak
	count := 0
	for _, candidate := range installed {
		id, valid := flatpakFullRef(candidate.Ref, "app")
		if !valid {
			return ResolvedFlatpak{}, UnresolvedFlatpakDeployment
		}
		if id != request.AppID {
			continue
		}
		if !validFlatpakScope(candidate.Scope, candidate.Installation, candidate.OwnerUID, binding.UID) {
			return ResolvedFlatpak{}, UnresolvedFlatpakDeployment
		}
		count++
		if count > 1 {
			return ResolvedFlatpak{}, UnresolvedFlatpakDeployment
		}
		copy := candidate
		selected = &copy
	}
	if count != 1 || selected == nil || selected.Ref != approval.Ref || selected.Scope != approval.Scope ||
		selected.Installation != approval.Installation || selected.OwnerUID != approval.OwnerUID ||
		selected.Origin != approval.Origin || selected.PublisherFingerprint != approval.PublisherFingerprint ||
		!hash.MatchString(selected.Commit) || !hash.MatchString(selected.RuntimeCommit) ||
		selected.RuntimeCommit == "" || selected.RuntimeRef == "" {
		return ResolvedFlatpak{}, UnresolvedFlatpakDeployment
	}
	if _, ok := flatpakFullRef(selected.RuntimeRef, "runtime"); !ok ||
		strings.Split(selected.RuntimeRef, "/")[2] != strings.Split(selected.Ref, "/")[2] {
		return ResolvedFlatpak{}, UnresolvedFlatpakDeployment
	}
	if approval.PublisherFingerprint == "" && selected.Commit != approval.Commit {
		return ResolvedFlatpak{}, UnresolvedFlatpakDeployment
	}
	return ResolvedFlatpak{InstalledFlatpak: *selected}, nil
}
