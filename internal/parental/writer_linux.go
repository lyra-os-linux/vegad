package parental

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"

	"golang.org/x/sys/unix"
)

const PublicationUncertain Code = "PublicationUncertain"

// CatalogWriter publishes only a negative ordinary-account state. It neither
// initializes missing authority nor recovers admission. Call after qualified
// authorization, outside the administrative guard's shared catalog lease.
// Directory ancestry and cooperating root writers remain trusted.
type CatalogWriter struct {
	directory  string
	owner      uint32
	sync       func(int) error
	checkpoint func(string) // private fault/crash injection; nil in production
}

func NewCatalogWriter(directory string) *CatalogWriter {
	return &CatalogWriter{directory: directory, owner: 0, sync: unix.Fsync}
}

// encodeCatalog validates its own output through the closed decoder. Rebuilding
// ordinary authority from NSS, changing policy or dropping tombstones is absent.
func encodeCatalog(c Catalog) ([]byte, Catalog, error) {
	accounts := make([]map[string]any, 0, len(c.accounts))
	for _, a := range c.accounts {
		adapters := map[string]string{}
		for _, v := range a.Adapters {
			adapters[v.Name] = v.Digest
		}
		accounts = append(accounts, map[string]any{"association": a.Association, "generation": a.Generation, "identity": a.Identity, "revision": a.Revision, "kind": a.Kind, "state": a.State, "operation": a.Operation, "barrier": a.Barrier, "policy": a.Policy, "adapters": adapters})
	}
	data, err := json.Marshal(map[string]any{"schema": 1, "revision": c.revision, "accounts": accounts})
	if err != nil {
		return nil, Catalog{}, InvalidCatalog
	}
	decoded, err := DecodeCatalog(data)
	return data, decoded, err
}

// BlockOrdinary requires the complete expected snapshot. An already blocked
// record for this same operation is an unchanged result only when that current
// snapshot is supplied. Replaying the pre-commit snapshot conflicts. No record
// or lock is created on absence; no source or temporary file is adopted.
func (w *CatalogWriter) BlockOrdinary(expected Catalog, association, operation string) (result Catalog, err error) {
	if w == nil || w.sync == nil {
		return Catalog{}, RecoveryRequired
	}
	if !token.MatchString(association) || !token.MatchString(operation) || expected.revision == 0 || !hash.MatchString(expected.digest) {
		return Catalog{}, Conflict
	}
	lease, err := openLease(w.directory, "catalog.lock", w.owner, true, UnsafeCatalog)
	if err != nil {
		return Catalog{}, err
	}
	published := false
	defer func() {
		if e := lease.close(); e != nil && err == nil {
			err = StorageFailure
		}
		if err != nil {
			result = Catalog{}
			if published {
				err = PublicationUncertain
			}
		}
	}()
	old, err := lease.read("catalog.json", CatalogLimit)
	if err != nil {
		return Catalog{}, err
	}
	current, err := DecodeCatalog(old)
	if err != nil {
		return Catalog{}, err
	}
	if current.revision != expected.revision || current.digest != expected.digest {
		return Catalog{}, Conflict
	}
	index := -1
	for i, a := range current.accounts {
		if a.Association == association {
			index = i
			break
		}
	}
	if index < 0 {
		return Catalog{}, RecoveryRequired
	}
	a := current.accounts[index]
	if a.Kind != "ordinary" {
		return Catalog{}, UnsupportedOperation
	}
	if a.State == "recovering" && a.Barrier && a.Operation == operation {
		if err = lease.stable(); err != nil {
			return Catalog{}, err
		}
		published = true
		if err = w.sync(lease.dir); err != nil {
			return Catalog{}, PublicationUncertain
		}
		return current, nil
	}
	if a.State != "active" || a.Barrier || a.Operation != "" {
		return Catalog{}, Blocked
	}
	if current.revision >= math.MaxInt64 {
		return Catalog{}, Conflict
	}
	// Operation IDs remain owned by their existing record, including tombstones.
	for _, other := range current.accounts {
		if other.Operation == operation {
			return Catalog{}, Conflict
		}
	}
	current.revision++
	a.Revision = current.revision
	a.State = "recovering"
	a.Barrier = true
	a.Operation = operation
	current.accounts[index] = a
	data, next, err := encodeCatalog(current)
	if err != nil {
		return Catalog{}, err
	}
	var original unix.Stat_t
	if unix.Fstatat(lease.dir, "catalog.json", &original, unix.AT_SYMLINK_NOFOLLOW) != nil || !safeFile(&original, w.owner) {
		return Catalog{}, UnsafeCatalog
	}
	name, err := randomIdentifier()
	if err != nil {
		return Catalog{}, err
	}
	name = ".catalog-" + name + ".tmp"
	fd, err := unix.Openat(lease.dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return Catalog{}, StorageFailure
	}
	var temp unix.Stat_t
	defer func() {
		if fd >= 0 && unix.Close(fd) != nil && err == nil {
			err = StorageFailure
		}
		if sameEntry(lease.dir, name, &temp) {
			if e := unix.Unlinkat(lease.dir, name, 0); e != nil && err == nil {
				err = StorageFailure
			}
		}
	}()
	if unix.Fchmod(fd, 0600) != nil || unix.Fstat(fd, &temp) != nil || !safeFile(&temp, w.owner) {
		return Catalog{}, UnsafeCatalog
	}
	for offset := 0; offset < len(data); {
		n, e := unix.Write(fd, data[offset:])
		if errors.Is(e, unix.EINTR) {
			continue
		}
		if e != nil || n <= 0 {
			return Catalog{}, StorageFailure
		}
		offset += n
	}
	if err = w.sync(fd); err != nil {
		return Catalog{}, StorageFailure
	}
	if w.checkpoint != nil {
		w.checkpoint("file-synced")
	}
	if err = unix.Close(fd); err != nil {
		fd = -1
		return Catalog{}, StorageFailure
	}
	fd = -1
	if err = lease.stable(); err != nil {
		return Catalog{}, err
	}
	actual, err := lease.read("catalog.json", CatalogLimit)
	if err != nil {
		return Catalog{}, err
	}
	if !bytes.Equal(old, actual) || !sameEntry(lease.dir, "catalog.json", &original) || !sameEntry(lease.dir, name, &temp) {
		return Catalog{}, Conflict
	}
	if err = unix.Renameat(lease.dir, name, lease.dir, "catalog.json"); err != nil {
		return Catalog{}, StorageFailure
	}
	published = true
	if w.checkpoint != nil {
		w.checkpoint("renamed")
	}
	if err = w.sync(lease.dir); err != nil {
		return Catalog{}, PublicationUncertain
	}
	if w.checkpoint != nil {
		w.checkpoint("directory-synced")
	}
	if err = lease.stable(); err != nil {
		return Catalog{}, err
	}
	actual, err = lease.read("catalog.json", CatalogLimit)
	if err != nil {
		return Catalog{}, err
	}
	if !bytes.Equal(data, actual) {
		return Catalog{}, PublicationUncertain
	}
	return next, nil
}
