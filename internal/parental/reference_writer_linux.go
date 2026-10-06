package parental

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const referenceTransactionName = ".reference-transaction.json"
const referenceTransactionLimit = 2*ReferenceLimit + 1024

type referenceTransaction struct {
	issuer, operation string
	before, after     RecoveryReference
}

func checkedReference(ref RecoveryReference) ([]byte, error) {
	data, e := referenceBytes(ref)
	if e != nil {
		return nil, InvalidReference
	}
	checked, e := DecodeRecoveryReference(data)
	if e != nil || checked.digest != ref.digest {
		return nil, InvalidReference
	}
	return data, nil
}
func referenceAdvance(before, after RecoveryReference) bool {
	return before.installation == after.installation && before.sequence < math.MaxInt64 && after.sequence == before.sequence+1 && after.mode == "blocked-recovery" && after.catalog.revision > before.catalog.revision && preservesOwnership(before.catalog, after.catalog)
}
func transactionBytes(t referenceTransaction) ([]byte, error) {
	before, e := checkedReference(t.before)
	if e != nil {
		return nil, e
	}
	after, e := checkedReference(t.after)
	if e != nil {
		return nil, e
	}
	return json.Marshal(map[string]any{"schema": 1, "issuer": t.issuer, "operation": t.operation, "before": json.RawMessage(before), "after": json.RawMessage(after)})
}
func decodeReferenceTransaction(data []byte) (referenceTransaction, error) {
	fail := referenceTransaction{}
	if len(data) == 0 || len(data) > referenceTransactionLimit || !utf8.Valid(data) {
		return fail, InvalidReference
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	raw, e := readJSON(d, 0)
	if e != nil {
		return fail, InvalidReference
	}
	if _, e = d.Token(); e != io.EOF {
		return fail, InvalidReference
	}
	row, ok := object(raw, "schema", "issuer", "operation", "before", "after")
	if !ok {
		return fail, InvalidReference
	}
	if n, ok := positive(row["schema"], 1); !ok || n != 1 {
		return fail, InvalidReference
	}
	t := referenceTransaction{}
	if t.issuer, ok = matching(row["issuer"], token); !ok {
		return fail, InvalidReference
	}
	if t.operation, ok = matching(row["operation"], token); !ok {
		return fail, InvalidReference
	}
	for _, p := range []struct {
		key string
		ref *RecoveryReference
	}{{"before", &t.before}, {"after", &t.after}} {
		data, e := json.Marshal(row[p.key])
		if e != nil {
			return fail, InvalidReference
		}
		*p.ref, e = DecodeRecoveryReference(data)
		if e != nil {
			return fail, InvalidReference
		}
	}
	if !referenceAdvance(t.before, t.after) {
		return fail, Conflict
	}
	return t, nil
}
func referencePinBytes(ref RecoveryReference, issuer string) []byte {
	data, _ := json.Marshal(map[string]any{"schema": 1, "installation": ref.installation, "issuer": issuer, "sequence": ref.sequence, "reference_digest": ref.digest})
	return data
}
func noReferenceTransaction(l *directoryLease) error {
	var s unix.Stat_t
	e := unix.Fstatat(l.dir, referenceTransactionName, &s, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(e, unix.ENOENT) {
		_, e = enrollmentState(l)
		return e
	}
	return RecoveryRequired
}

// ReferenceWriter advances an EXISTING issuer store, never provisions missing
// directories/locks/pins or infers ownership from NSS. Issuer completeness and
// authorization are trusted caller contracts, not proven by decoded JSON.
// revalidate must be a bounded fresh maintenance check, AFTER any Polkit dialog;
// it runs before storage and under EX. No dialogs or catalog SH lease may be held.
// Root, ancestry, qualifier and cooperating same-inode writers are trusted.
type ReferenceWriter struct {
	directory, catalogDirectory, issuer string
	owner                               uint32
	qualify                             func(string, string) (string, error)
	sync                                func(int) error
	checkpoint                          func(string) // private fault/process-cut injection only
}

func NewReferenceWriter(directory, catalogDirectory, issuer string, qualify func(string, string) (string, error)) *ReferenceWriter {
	return &ReferenceWriter{directory: directory, catalogDirectory: catalogDirectory, issuer: issuer, owner: 0, qualify: qualify, sync: unix.Fsync}
}
func (w *ReferenceWriter) gate(revalidate func() error) (string, error) {
	if revalidate == nil || revalidate() != nil {
		return "", Denied
	}
	if w == nil || w.qualify == nil || w.sync == nil || !token.MatchString(w.issuer) || !canonicalDomainPath(w.directory) || !canonicalDomainPath(w.catalogDirectory) || w.directory == w.catalogDirectory {
		return "", RecoveryRequired
	}
	boundary, e := w.qualify(w.directory, w.catalogDirectory)
	if e != nil || !hash.MatchString(boundary) {
		return "", RecoveryRequired
	}
	return boundary, nil
}
func (w *ReferenceWriter) fresh(l *directoryLease, boundary string, revalidate func() error) error {
	if e := l.stable(); e != nil {
		return e
	}
	after, e := w.qualify(w.directory, w.catalogDirectory)
	if e != nil || after != boundary {
		return Conflict
	}
	if revalidate() == nil {
		return l.stable()
	}
	return Denied
}
func (w *ReferenceWriter) point(stage string) {
	if w.checkpoint != nil {
		w.checkpoint(stage)
	}
}

// replace stages a new private inode and checks the exact prior bytes/entry
// immediately before rename. A journal uses RENAME_NOREPLACE; no orphan temp is
// adopted. Every renamed entry is directory-synced before the next stage.
func (w *ReferenceWriter) replace(l *directoryLease, name string, prior, data []byte, limit int) (renamed bool, err error) {
	var original unix.Stat_t
	if prior != nil {
		old, e := l.read(name, limit)
		if e != nil {
			return false, e
		}
		if !bytes.Equal(old, prior) {
			return false, Conflict
		}
		if unix.Fstatat(l.dir, name, &original, unix.AT_SYMLINK_NOFOLLOW) != nil || !safeFile(&original, w.owner) {
			return false, UnsafeState
		}
	} else {
		var s unix.Stat_t
		if e := unix.Fstatat(l.dir, name, &s, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(e, unix.ENOENT) {
			return false, Conflict
		}
	}
	id, e := randomIdentifier()
	if e != nil {
		return false, e
	}
	tempName := ".reference-" + id + ".tmp"
	fd, e := unix.Openat(l.dir, tempName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		return false, StorageFailure
	}
	var temp unix.Stat_t
	defer func() {
		if fd >= 0 && unix.Close(fd) != nil && err == nil {
			err = StorageFailure
		}
		if sameEntry(l.dir, tempName, &temp) {
			if e := unix.Unlinkat(l.dir, tempName, 0); e != nil && err == nil {
				err = StorageFailure
			}
		}
	}()
	if unix.Fchmod(fd, 0600) != nil || unix.Fstat(fd, &temp) != nil || !safeFile(&temp, w.owner) {
		return false, UnsafeState
	}
	for offset := 0; offset < len(data); {
		n, e := unix.Write(fd, data[offset:])
		if errors.Is(e, unix.EINTR) {
			continue
		}
		if e != nil || n <= 0 {
			return false, StorageFailure
		}
		offset += n
	}
	if w.sync(fd) != nil {
		return false, StorageFailure
	}
	if e := unix.Close(fd); e != nil {
		fd = -1
		return false, StorageFailure
	}
	fd = -1
	if e := l.stable(); e != nil {
		return false, e
	}
	if !sameEntry(l.dir, tempName, &temp) {
		return false, Conflict
	}
	if prior != nil {
		old, e := l.read(name, limit)
		if e != nil {
			return false, e
		}
		if !bytes.Equal(old, prior) || !sameEntry(l.dir, name, &original) {
			return false, Conflict
		}
		e = unix.Renameat(l.dir, tempName, l.dir, name)
	} else {
		e = unix.Renameat2(l.dir, tempName, l.dir, name, unix.RENAME_NOREPLACE)
	}
	if e != nil {
		return false, StorageFailure
	}
	renamed = true
	w.point(name + ":renamed")
	if w.sync(l.dir) != nil {
		return true, PublicationUncertain
	}
	w.point(name + ":durable")
	return true, nil
}
func (w *ReferenceWriter) pair(l *directoryLease) ([]byte, []byte, RecoveryReference, referencePin, error) {
	data, e := l.read("reference.json", ReferenceLimit)
	if e != nil {
		return nil, nil, RecoveryReference{}, referencePin{}, e
	}
	ref, e := DecodeRecoveryReference(data)
	if e != nil {
		return nil, nil, ref, referencePin{}, e
	}
	pinData, e := l.read("current.json", referencePinLimit)
	if e != nil {
		return nil, nil, ref, referencePin{}, e
	}
	pin, e := decodeReferencePin(pinData)
	if e == nil {
		e = enrollmentMatches(l, ref, pin)
	}
	return data, pinData, ref, pin, e
}
func pinMatches(pin referencePin, ref RecoveryReference, issuer string) bool {
	return pin.issuer == issuer && pin.installation == ref.installation && pin.sequence == ref.sequence && pin.digest == ref.digest
}
func (w *ReferenceWriter) finish(l *directoryLease, t referenceTransaction, journal []byte, boundary string, revalidate func() error) error {
	refData, pinData, ref, pin, e := w.pair(l)
	if e != nil {
		return e
	}
	// Only protocol-reachable pairs: old/old, new/old, new/new. Never roll back
	// new pin to old reference or recreate missing canonical records.
	if ref.digest != t.before.digest && ref.digest != t.after.digest {
		return Conflict
	}
	oldPin, newPin := pinMatches(pin, t.before, w.issuer), pinMatches(pin, t.after, w.issuer)
	if (!oldPin && !newPin) || (newPin && ref.digest != t.after.digest) {
		return Conflict
	}
	if e = w.fresh(l, boundary, revalidate); e != nil {
		return e
	}
	nextData, e := checkedReference(t.after)
	if e != nil {
		return e
	}
	if ref.digest != t.after.digest {
		if _, e = w.replace(l, "reference.json", refData, nextData, ReferenceLimit); e != nil {
			return e
		}
	}
	if !newPin {
		if _, e = w.replace(l, "current.json", pinData, referencePinBytes(t.after, w.issuer), referencePinLimit); e != nil {
			return e
		}
	}
	// Reconfirm pair, journal, layout and maintenance before clearing the barrier.
	actualJournal, e := l.read(referenceTransactionName, referenceTransactionLimit)
	if e != nil || !bytes.Equal(journal, actualJournal) {
		return Conflict
	}
	_, _, ref, pin, e = w.pair(l)
	if e != nil || ref.digest != t.after.digest || !pinMatches(pin, t.after, w.issuer) {
		return Conflict
	}
	if e = w.fresh(l, boundary, revalidate); e != nil {
		return e
	}
	if w.sync(l.dir) != nil {
		return PublicationUncertain
	}
	if unix.Unlinkat(l.dir, referenceTransactionName, 0) != nil {
		return StorageFailure
	}
	w.point("journal:removed")
	if w.sync(l.dir) != nil {
		return PublicationUncertain
	}
	w.point("commit:durable")
	return l.stable()
}

func (w *ReferenceWriter) Advance(expected, next RecoveryReference, operation string, revalidate func() error) (result RecoveryReference, err error) {
	boundary, e := w.gate(revalidate)
	if e != nil {
		return result, e
	}
	if !token.MatchString(operation) {
		return result, Conflict
	}
	if _, e = checkedReference(expected); e != nil {
		return result, e
	}
	if _, e = checkedReference(next); e != nil {
		return result, e
	}
	if !referenceAdvance(expected, next) {
		return result, Conflict
	}
	l, e := openLease(w.directory, "reference.lock", w.owner, true, UnsafeState)
	if e != nil {
		return result, e
	}
	published := false
	defer func() {
		if e := l.close(); e != nil && err == nil {
			err = e
		}
		if err != nil {
			result = RecoveryReference{}
			if published {
				err = PublicationUncertain
			}
		}
	}()
	if e = noReferenceTransaction(l); e != nil {
		return result, e
	}
	_, _, current, pin, e := w.pair(l)
	if e != nil {
		return result, e
	}
	if current.digest != expected.digest || !pinMatches(pin, expected, w.issuer) {
		return result, Conflict
	}
	if e = w.fresh(l, boundary, revalidate); e != nil {
		return result, e
	}
	t := referenceTransaction{w.issuer, operation, expected, next}
	journal, e := transactionBytes(t)
	if e != nil {
		return result, e
	}
	published, e = w.replace(l, referenceTransactionName, nil, journal, referenceTransactionLimit)
	if e != nil {
		return result, e
	}
	if e = w.finish(l, t, journal, boundary, revalidate); e != nil {
		return result, e
	}
	return next, nil
}

// Resume requires fresh authorization for the exact proposed reference and
// operation. A pending transaction is never automatically cleared by a reader.
func (w *ReferenceWriter) Resume(next RecoveryReference, operation string, revalidate func() error) (result RecoveryReference, err error) {
	boundary, e := w.gate(revalidate)
	if e != nil {
		return result, e
	}
	if !token.MatchString(operation) {
		return result, Conflict
	}
	if _, e = checkedReference(next); e != nil {
		return result, e
	}
	l, e := openLease(w.directory, "reference.lock", w.owner, true, UnsafeState)
	if e != nil {
		return result, e
	}
	finishing := false
	defer func() {
		if e := l.close(); e != nil && err == nil {
			err = e
		}
		if err != nil {
			result = RecoveryReference{}
			if finishing {
				err = PublicationUncertain
			}
		}
	}()
	journal, e := l.read(referenceTransactionName, referenceTransactionLimit)
	if e != nil {
		return result, e
	}
	t, e := decodeReferenceTransaction(journal)
	if e != nil {
		return result, e
	}
	if t.issuer != w.issuer || t.operation != operation || t.after.digest != next.digest {
		return result, Conflict
	}
	finishing = true
	if e = w.finish(l, t, journal, boundary, revalidate); e != nil {
		return result, PublicationUncertain
	}
	return next, nil
}
