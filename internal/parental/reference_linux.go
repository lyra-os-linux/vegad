package parental

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"path/filepath"
	"unicode/utf8"
)

const referencePinLimit = 4096

type referencePin struct {
	installation, issuer, digest string
	sequence                     uint64
}

func decodeReferencePin(data []byte) (referencePin, error) {
	if len(data) == 0 || len(data) > referencePinLimit || !utf8.Valid(data) {
		return referencePin{}, InvalidReference
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	raw, e := readJSON(d, 0)
	if e != nil {
		return referencePin{}, InvalidReference
	}
	if _, e = d.Token(); e != io.EOF {
		return referencePin{}, InvalidReference
	}
	row, ok := object(raw, "schema", "installation", "issuer", "sequence", "reference_digest")
	if !ok {
		return referencePin{}, InvalidReference
	}
	v, ok := positive(row["schema"], 1)
	if !ok || v != 1 {
		return referencePin{}, InvalidReference
	}
	p := referencePin{}
	if p.installation, ok = matching(row["installation"], token); !ok {
		return p, InvalidReference
	}
	if p.issuer, ok = matching(row["issuer"], token); !ok {
		return p, InvalidReference
	}
	if p.sequence, ok = positive(row["sequence"], math.MaxInt64); !ok {
		return p, InvalidReference
	}
	if p.digest, ok = matching(row["reference_digest"], hash); !ok {
		return p, InvalidReference
	}
	return p, nil
}

// FileReferenceSource reads a pre-provisioned maintenance authority. The issuer
// token is trusted service configuration, not a claim received from the caller.
// Root ownership authenticates storage only within the trusted-root maintenance
// model; it does not constitute a cryptographic signature or prove completeness.
//
// qualify must supply a validated storage-boundary fingerprint (SHA256), stable
// during the read. It must qualify the authority against the catalog's supported
// rollback domain. Merely differing paths/mount IDs or placing data in /var/lib
// is insufficient. A nil/unqualified callback always refuses, with no default.
// Ancestors, cooperating issuer writers and the qualifier are trusted adapters.
type FileReferenceSource struct {
	directory, catalogDirectory, issuer string
	owner                               uint32
	qualify                             func(string, string) (string, error)
}

func NewFileReferenceSource(directory, catalogDirectory, issuer string, qualify func(string, string) (string, error)) *FileReferenceSource {
	return &FileReferenceSource{directory: directory, catalogDirectory: catalogDirectory, issuer: issuer, owner: 0, qualify: qualify}
}
func (s *FileReferenceSource) Snapshot(installation string) (reference RecoveryReference, err error) {
	if s == nil || s.qualify == nil || !token.MatchString(s.issuer) {
		return RecoveryReference{}, RecoveryRequired
	}
	if !token.MatchString(installation) {
		return RecoveryReference{}, InvalidReference
	}
	if !filepath.IsAbs(s.directory) || !filepath.IsAbs(s.catalogDirectory) || filepath.Clean(s.directory) == filepath.Clean(s.catalogDirectory) {
		return RecoveryReference{}, RecoveryRequired
	}
	boundary, e := s.qualify(s.directory, s.catalogDirectory)
	if e != nil || !hash.MatchString(boundary) {
		return RecoveryReference{}, RecoveryRequired
	}
	lease, err := openLease(s.directory, "reference.lock", s.owner, false, UnsafeState)
	if err != nil {
		return RecoveryReference{}, err
	}
	defer func() {
		if e := lease.close(); e != nil && err == nil {
			reference = RecoveryReference{}
			err = e
		}
	}()
	if err = noReferenceTransaction(lease); err != nil {
		return RecoveryReference{}, err
	}
	currentData, err := lease.read("current.json", referencePinLimit)
	if err != nil {
		return RecoveryReference{}, err
	}
	pin, err := decodeReferencePin(currentData)
	if err != nil {
		return RecoveryReference{}, err
	}
	if pin.installation != installation || pin.issuer != s.issuer {
		return RecoveryReference{}, Conflict
	}
	data, err := lease.read("reference.json", ReferenceLimit)
	if err != nil {
		return RecoveryReference{}, err
	}
	reference, err = DecodeRecoveryReference(data)
	if err != nil {
		return RecoveryReference{}, err
	}
	if reference.installation != installation || reference.sequence != pin.sequence || reference.digest != pin.digest {
		return RecoveryReference{}, Conflict
	}
	if err = enrollmentMatches(lease, reference, pin); err != nil {
		return RecoveryReference{}, err
	}
	// Read the pin again while holding the same shared lease. Noncooperating root
	// writers are not made trustworthy by this check; mismatches still refuse.
	afterData, err := lease.read("current.json", referencePinLimit)
	if err != nil {
		return RecoveryReference{}, err
	}
	if !bytes.Equal(currentData, afterData) {
		return RecoveryReference{}, Conflict
	}
	after, e := s.qualify(s.directory, s.catalogDirectory)
	if e != nil || after != boundary {
		return RecoveryReference{}, Conflict
	}
	if err = noReferenceTransaction(lease); err != nil {
		return RecoveryReference{}, err
	}
	if err = lease.stable(); err != nil {
		return RecoveryReference{}, err
	}
	return reference, nil
}
