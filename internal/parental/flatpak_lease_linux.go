package parental

import "strconv"

// WithFlatpakLease revalidates a prior socket binding under the same catalog,
// inventory and per-UID gate locks used by parental mutations. The visitor is
// synchronous and must not return until the launch admission point has been
// crossed. This is not a launcher or an app-policy check.
func (b *FileLeases) WithFlatpakLease(binding FlatpakPeerBinding, peer FlatpakLaunchPeer,
	observeIdentity func(uint32) (Identity, error), required []string,
	observeAdapters func(Account) (Applied, error), observeDomain func(Account) (string, error),
	visit func(FlatpakPeerBinding) error) (err error) {
	if b == nil || b.decodeProof == nil || visit == nil ||
		!token.MatchString(binding.Association) || !token.MatchString(binding.Generation) ||
		!hash.MatchString(binding.CatalogDigest) || binding.CatalogRevision == 0 ||
		peer.UID != binding.UID || peer.GID != binding.GID || peer.SELinuxContext != binding.SELinuxContext {
		return Denied
	}
	var leases []*directoryLease
	defer func() {
		for n := len(leases) - 1; n >= 0; n-- {
			if closeErr := leases[n].close(); err == nil && closeErr != nil {
				err = closeErr
			}
		}
	}()
	acquire := func(path, name string, unsafe Code) (*directoryLease, error) {
		l, e := openLease(path, name, b.owner, false, unsafe)
		if e == nil {
			leases = append(leases, l)
		}
		return l, e
	}
	catalog, e := acquire(b.catalog, "catalog.lock", UnsafeCatalog)
	if e != nil {
		return e
	}
	data, e := catalog.read("catalog.json", CatalogLimit)
	if e != nil {
		return e
	}
	c, e := DecodeCatalog(data)
	if e != nil {
		return e
	}
	if c.digest != binding.CatalogDigest || c.revision != binding.CatalogRevision {
		return Conflict
	}
	var account *Account
	for _, a := range c.accounts {
		if a.Identity.UID == peer.UID {
			copy := cloneAccount(a)
			account = &copy
			break
		}
	}
	if account == nil || account.Association != binding.Association || account.Generation != binding.Generation {
		return Conflict
	}
	uid := strconv.FormatUint(uint64(peer.UID), 10)
	inventory, e := acquire(b.inventory, ".inventory-"+uid+".lock", UnsafeState)
	if e != nil {
		return e
	}
	ledger, e := acquire(b.ledger, ".gate-"+uid+".lock", UnsafeState)
	if e != nil {
		return e
	}
	if e = negativeEntry(ledger, uid); e != nil {
		return e
	}
	data, e = inventory.read(uid+".json", CatalogLimit)
	if e != nil {
		return e
	}
	if _, e = registeredInventory(data, account.Identity); e != nil {
		return e
	}
	proof, e := ledger.read(uid, 8192)
	if e != nil {
		return e
	}
	identity, e := b.decodeProof(append([]byte(nil), proof...), peer.UID)
	if e != nil || identity != account.Identity {
		return InvalidIdentity
	}
	if e = negativeEntry(ledger, uid); e != nil {
		return e
	}
	for _, l := range leases {
		if e = l.stable(); e != nil {
			return e
		}
	}
	fresh, e := BindFlatpakLaunchPeer(c, peer, observeIdentity, required, observeAdapters, observeDomain)
	if e != nil {
		return e
	}
	if fresh != binding {
		return Conflict
	}
	if e = visit(fresh); e != nil {
		return e
	}
	if e = negativeEntry(ledger, uid); e != nil {
		return e
	}
	for _, l := range leases {
		if e = l.stable(); e != nil {
			return e
		}
	}
	return nil
}
