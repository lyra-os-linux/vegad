package parental

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"golang.org/x/sys/unix"
	"strconv"
)

const UnsafeState Code = "UnsafeState"

// ProofDecoder must decode the authoritative proof format and reject ambiguous
// records. No default JSON/PAM decoder is installed; identity is checked here.
type ProofDecoder func([]byte, uint32) (Identity, error)
type FileLeases struct {
	catalog, inventory, ledger string
	owner                      uint32
	decodeProof                ProofDecoder
}

func NewFileLeases(catalog, inventory, ledger string, decodeProof ProofDecoder) *FileLeases {
	return &FileLeases{catalog: catalog, inventory: inventory, ledger: ledger, owner: 0, decodeProof: decodeProof}
}
func negativeEntry(l *directoryLease, uid string) error {
	for _, name := range []string{".deny-" + uid, ".withdrawal-" + uid + ".json", ".operation-" + uid + ".json"} {
		var stat unix.Stat_t
		err := unix.Fstatat(l.dir, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
		if err == nil {
			return Blocked
		} // any presence wins, including corrupt/link/FIFO
		if err != unix.ENOENT {
			return l.unsafe
		}
	}
	return nil
}
func proofDigest(data []byte) string {
	h := sha256.New()
	h.Write([]byte("lyra-parental-guard-ledger-v1\x00"))
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(data)))
	h.Write(length[:])
	h.Write(data)
	h.Write([]byte("\x00deny-absent\x00withdrawal-absent\x00owner-absent"))
	return hex.EncodeToString(h.Sum(nil))
}

// WithLease performs only reads. Negative entries are checked before positive
// proofs and again after codecs. Catalog SH -> inventory SH -> operation SH/EX
// remain held through visit; the guard consumes before the final effect.
func (b *FileLeases) WithLease(plan Plan, exclusive bool, visit func(MutationState) error) (err error) {
	if b == nil || !token.MatchString(plan.identifier) || !token.MatchString(plan.association) || plan.account.Identity.UID == 0 {
		return Denied
	}
	if visit == nil {
		return StorageFailure
	}
	var leases []*directoryLease
	defer func() {
		for n := len(leases) - 1; n >= 0; n-- {
			if closeErr := leases[n].close(); err == nil && closeErr != nil {
				err = closeErr
			}
		}
	}()
	acquire := func(path, name string, exclusive bool, unsafe Code) (*directoryLease, error) {
		l, e := openLease(path, name, b.owner, exclusive, unsafe)
		if e == nil {
			leases = append(leases, l)
		}
		return l, e
	}
	catalog, e := acquire(b.catalog, "catalog.lock", false, UnsafeCatalog)
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
	if c.digest != plan.catalogDigest {
		return Conflict
	}
	uid := strconv.FormatUint(uint64(plan.account.Identity.UID), 10)
	inventory, e := acquire(b.inventory, ".inventory-"+uid+".lock", false, UnsafeState)
	if e != nil {
		return e
	}
	ledger, e := acquire(b.ledger, ".gate-"+uid+".lock", exclusive, UnsafeState)
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
	invDigest, e := registeredInventory(data, plan.account.Identity)
	if e != nil {
		return e
	}
	proof, e := ledger.read(uid, 8192)
	if e != nil {
		return e
	}
	if b.decodeProof == nil {
		return RecoveryRequired
	}
	identity, e := b.decodeProof(append([]byte(nil), proof...), plan.account.Identity.UID)
	if e != nil || identity != plan.account.Identity {
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
	state := MutationState{Catalog: c, InventoryDigest: invDigest, LedgerDigest: proofDigest(proof), Proof: &identity}
	if e = visit(state); e != nil {
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
