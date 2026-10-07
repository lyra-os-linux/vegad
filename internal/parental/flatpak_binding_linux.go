package parental

// FlatpakPeerBinding is a read-only observation of one catalog snapshot.
// It is not a launch capability: revocation, account barriers and policy
// changes must be rechecked under the launch operation's own gate.
type FlatpakPeerBinding struct {
	Association, Generation, CatalogDigest string
	CatalogRevision                        uint64
	UID, GID                               uint32
	SELinuxContext                         string
}

// BindFlatpakLaunchPeer ties a kernel-observed socket peer to an active
// supervised account and the exact applied producer generation. The domain
// observer must be server-owned and derive the expected context from the
// active account; a client-supplied context is never authoritative.
//
// This function does not establish who sent bytes on a transferred socket;
// the caller must use ReadFlatpakLaunchRequest first. It does not authorize
// an app, hold a policy lease or start a process.
func BindFlatpakLaunchPeer(c Catalog, peer FlatpakLaunchPeer,
	observeIdentity func(uint32) (Identity, error), required []string,
	observeAdapters func(Account) (Applied, error),
	observeDomain func(Account) (string, error)) (FlatpakPeerBinding, error) {
	if peer.PID <= 0 || peer.UID == 0 || peer.GID == 0 ||
		peer.SELinuxContext == "" || peer.SELinuxContext == "kernel" {
		return FlatpakPeerBinding{}, UnavailableFlatpakPeer
	}
	eligibility, err := Classify(c, peer.UID, observeIdentity, required, observeAdapters)
	if err != nil {
		return FlatpakPeerBinding{}, err
	}
	if eligibility != SupervisedEligible {
		return FlatpakPeerBinding{}, Blocked
	}
	for _, account := range c.accounts {
		if account.Identity.UID != peer.UID {
			continue
		}
		if account.Identity.GID != peer.GID || observeDomain == nil {
			return FlatpakPeerBinding{}, InvalidIdentity
		}
		expected, err := observeDomain(cloneAccount(account))
		if err != nil || expected == "" || expected == "kernel" || expected != peer.SELinuxContext {
			return FlatpakPeerBinding{}, InvalidIdentity
		}
		return FlatpakPeerBinding{
			Association: account.Association, Generation: account.Generation,
			CatalogDigest: c.digest, CatalogRevision: c.revision,
			UID: peer.UID, GID: peer.GID, SELinuxContext: expected,
		}, nil
	}
	return FlatpakPeerBinding{}, RecoveryRequired
}
