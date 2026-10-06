package parental

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const enrollmentName = "enrollment.json"
const enrollmentCommitName = "enrollment-committed.json"
const enrollmentLimit = ReferenceLimit + 1024
const enrollmentCommitLimit = 4096

type enrollment struct {
	issuer, operation string
	reference         RecoveryReference
}

func enrollmentBytes(s enrollment) ([]byte, error) {
	data, e := checkedReference(s.reference)
	if e != nil {
		return nil, e
	}
	return json.Marshal(map[string]any{"schema": 1, "issuer": s.issuer, "operation": s.operation, "reference": json.RawMessage(data)})
}
func enrollmentObject(data []byte, limit int, keys ...string) (map[string]any, error) {
	if len(data) == 0 || len(data) > limit || !utf8.Valid(data) {
		return nil, InvalidReference
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, e := readJSON(d, 0)
	if e != nil {
		return nil, InvalidReference
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, InvalidReference
	}
	row, ok := object(v, keys...)
	if !ok {
		return nil, InvalidReference
	}
	if n, ok := positive(row["schema"], 1); !ok || n != 1 {
		return nil, InvalidReference
	}
	return row, nil
}
func decodeEnrollment(data []byte) (enrollment, error) {
	row, e := enrollmentObject(data, enrollmentLimit, "schema", "issuer", "operation", "reference")
	if e != nil {
		return enrollment{}, e
	}
	s := enrollment{}
	var ok bool
	if s.issuer, ok = matching(row["issuer"], token); !ok {
		return s, InvalidReference
	}
	if s.operation, ok = matching(row["operation"], token); !ok {
		return s, InvalidReference
	}
	refData, e := json.Marshal(row["reference"])
	if e != nil {
		return s, InvalidReference
	}
	s.reference, e = DecodeRecoveryReference(refData)
	if e != nil || s.reference.mode != "first-install" {
		return enrollment{}, InvalidReference
	}
	return s, nil
}
func enrollmentCommitBytes(s enrollment, seal []byte) []byte {
	data, _ := json.Marshal(map[string]any{"schema": 1, "issuer": s.issuer, "installation": s.reference.installation, "operation": s.operation, "reference_digest": s.reference.digest, "seal_digest": fmtEnrollmentDigest(seal)})
	return data
}
func fmtEnrollmentDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func enrollmentPresent(l *directoryLease, name string) (bool, error) {
	var stat unix.Stat_t
	e := unix.Fstatat(l.dir, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(e, unix.ENOENT) {
		return false, nil
	}
	if e != nil {
		return false, UnsafeState
	}
	return true, nil
}

// No markers is legacy read compatibility, never evidence permitting enrollment.
func enrollmentState(l *directoryLease) (*enrollment, error) {
	sealExists, e := enrollmentPresent(l, enrollmentName)
	if e != nil {
		return nil, e
	}
	commitExists, e := enrollmentPresent(l, enrollmentCommitName)
	if e != nil {
		return nil, e
	}
	if !sealExists && !commitExists {
		return nil, nil
	}
	if !sealExists || !commitExists {
		return nil, RecoveryRequired
	}
	seal, e := l.read(enrollmentName, enrollmentLimit)
	if e != nil {
		return nil, e
	}
	s, e := decodeEnrollment(seal)
	if e != nil {
		return nil, e
	}
	commit, e := l.read(enrollmentCommitName, enrollmentCommitLimit)
	if e != nil {
		return nil, e
	}
	row, e := enrollmentObject(commit, enrollmentCommitLimit, "schema", "issuer", "installation", "operation", "reference_digest", "seal_digest")
	if e != nil {
		return nil, e
	}
	for k, want := range map[string]string{"issuer": s.issuer, "installation": s.reference.installation, "operation": s.operation, "reference_digest": s.reference.digest, "seal_digest": fmtEnrollmentDigest(seal)} {
		if got, ok := row[k].(string); !ok || got != want {
			return nil, Conflict
		}
	}
	return &s, nil
}
func enrollmentMatches(l *directoryLease, ref RecoveryReference, pin referencePin) error {
	s, e := enrollmentState(l)
	if e != nil {
		return e
	}
	if s == nil {
		return nil
	}
	if s.issuer != pin.issuer || s.reference.installation != ref.installation || ref.sequence < s.reference.sequence || (ref.sequence == s.reference.sequence && ref.digest != s.reference.digest) {
		return Conflict
	}
	return nil
}

// ReferenceProvisioner enrolls only a pre-provisioned, qualified directory with
// its stable reference.lock. proveFresh is a trusted, bounded, side-effect-free
// INSTALLER attestation check bound to installation, operation and full digest.
// Neither an empty directory nor root/wheel/NSS constitutes that attestation.
// No production attestation adapter is supplied here. No dialogs under EX.
type ReferenceProvisioner struct {
	writer     *ReferenceWriter
	proveFresh func(string, string, string) error
}

func NewReferenceProvisioner(directory, catalogDirectory, issuer string, qualify func(string, string) (string, error), proveFresh func(string, string, string) error) *ReferenceProvisioner {
	return &ReferenceProvisioner{NewReferenceWriter(directory, catalogDirectory, issuer, qualify), proveFresh}
}
func (p *ReferenceProvisioner) check(initial RecoveryReference, operation string, revalidate func() error) error {
	if revalidate == nil || revalidate() != nil {
		return Denied
	}
	if _, e := checkedReference(initial); e != nil {
		return e
	}
	if initial.mode != "first-install" || !token.MatchString(operation) {
		return Conflict
	}
	if p == nil || p.writer == nil || p.proveFresh == nil || p.proveFresh(initial.installation, operation, initial.digest) != nil {
		return Denied
	}
	return nil
}
func (p *ReferenceProvisioner) fresh(l *directoryLease, boundary string, s enrollment, revalidate func() error) error {
	if e := p.writer.fresh(l, boundary, revalidate); e != nil {
		return e
	}
	if p.proveFresh(s.reference.installation, s.operation, s.reference.digest) != nil {
		return Denied
	}
	return l.stable()
}
func onlyEnrollmentLock(l *directoryLease) error {
	fd, e := unix.Openat(l.dir, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		return UnsafeState
	}
	f := os.NewFile(uintptr(fd), "enrollment-directory")
	names, e := f.Readdirnames(2)
	closeErr := f.Close()
	if e != nil && e != io.EOF {
		return UnsafeState
	}
	if closeErr != nil {
		return UnsafeState
	}
	if len(names) != 1 || names[0] != "reference.lock" {
		return Conflict
	}
	return nil
}
func (p *ReferenceProvisioner) finish(l *directoryLease, s enrollment, seal []byte, boundary string, revalidate func() error) error {
	w := p.writer
	present, e := enrollmentPresent(l, enrollmentCommitName)
	if e != nil {
		return e
	}
	if present {
		return Conflict
	} // completed enrollment is NEVER repair/reseed
	refPresent, e := enrollmentPresent(l, "reference.json")
	if e != nil {
		return e
	}
	pinPresent, e := enrollmentPresent(l, "current.json")
	if e != nil {
		return e
	}
	if pinPresent && !refPresent {
		return Conflict
	}
	data, e := checkedReference(s.reference)
	if e != nil {
		return e
	}
	if refPresent {
		actual, e := l.read("reference.json", ReferenceLimit)
		if e != nil {
			return e
		}
		ref, e := DecodeRecoveryReference(actual)
		if e != nil || ref.digest != s.reference.digest {
			return Conflict
		}
	}
	if pinPresent {
		actual, e := l.read("current.json", referencePinLimit)
		if e != nil {
			return e
		}
		pin, e := decodeReferencePin(actual)
		if e != nil || !pinMatches(pin, s.reference, w.issuer) {
			return Conflict
		}
	}
	if e = p.fresh(l, boundary, s, revalidate); e != nil {
		return e
	}
	if !refPresent {
		if _, e = w.replace(l, "reference.json", nil, data, ReferenceLimit); e != nil {
			return e
		}
	}
	if !pinPresent {
		if _, e = w.replace(l, "current.json", nil, referencePinBytes(s.reference, w.issuer), referencePinLimit); e != nil {
			return e
		}
	}
	actual, e := l.read(enrollmentName, enrollmentLimit)
	if e != nil || !bytes.Equal(actual, seal) {
		return Conflict
	}
	actual, e = l.read("reference.json", ReferenceLimit)
	if e != nil {
		return e
	}
	ref, e := DecodeRecoveryReference(actual)
	if e != nil || ref.digest != s.reference.digest {
		return Conflict
	}
	actual, e = l.read("current.json", referencePinLimit)
	if e != nil {
		return e
	}
	pin, e := decodeReferencePin(actual)
	if e != nil || !pinMatches(pin, s.reference, w.issuer) {
		return Conflict
	}
	if e = p.fresh(l, boundary, s, revalidate); e != nil {
		return e
	}
	if _, e = w.replace(l, enrollmentCommitName, nil, enrollmentCommitBytes(s, seal), enrollmentCommitLimit); e != nil {
		return e
	}
	w.point("enrollment:committed")
	return enrollmentMatches(l, s.reference, pin)
}
func (p *ReferenceProvisioner) Initialize(initial RecoveryReference, operation string, revalidate func() error) (result RecoveryReference, err error) {
	if e := p.check(initial, operation, revalidate); e != nil {
		return result, e
	}
	w := p.writer
	boundary, e := w.gate(revalidate)
	if e != nil {
		return result, e
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
	if e = onlyEnrollmentLock(l); e != nil {
		return result, e
	}
	s := enrollment{w.issuer, operation, initial}
	seal, e := enrollmentBytes(s)
	if e != nil {
		return result, e
	}
	if e = p.fresh(l, boundary, s, revalidate); e != nil {
		return result, e
	}
	published, e = w.replace(l, enrollmentName, nil, seal, enrollmentLimit)
	if e != nil {
		return result, e
	}
	if e = p.finish(l, s, seal, boundary, revalidate); e != nil {
		return result, e
	}
	return initial, nil
}

// ResumeInitial only completes a matching interrupted first enrollment while
// the installer STILL attests freshness. A permanent commit marker forbids repair
// or resetting sequence 1 even when canonical files/lock have been lost.
func (p *ReferenceProvisioner) ResumeInitial(initial RecoveryReference, operation string, revalidate func() error) (result RecoveryReference, err error) {
	if e := p.check(initial, operation, revalidate); e != nil {
		return result, e
	}
	w := p.writer
	boundary, e := w.gate(revalidate)
	if e != nil {
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
	if present, e := enrollmentPresent(l, enrollmentCommitName); e != nil {
		return result, e
	} else if present {
		return result, Conflict
	}
	// no update transaction is permitted, including unsafe entries.
	if exists, e := enrollmentPresent(l, referenceTransactionName); e != nil {
		return result, e
	} else if exists {
		return result, RecoveryRequired
	}
	seal, e := l.read(enrollmentName, enrollmentLimit)
	if e != nil {
		return result, e
	}
	s, e := decodeEnrollment(seal)
	if e != nil {
		return result, e
	}
	if s.issuer != w.issuer || s.operation != operation || s.reference.digest != initial.digest {
		return result, Conflict
	}
	finishing = true
	if e = p.finish(l, s, seal, boundary, revalidate); e != nil {
		return result, e
	}
	return initial, nil
}
