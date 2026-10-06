package parental

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"reflect"
	"unicode/utf8"
)

const InvalidReference Code = "InvalidReference"
const ReferenceLimit = CatalogLimit + 1024

// RecoveryReference is an immutable, strictly decoded issuer statement. Decoding
// does NOT authenticate its issuer. Obtain it only from a configured trusted
// source independent of the catalog being repaired; never from a GUI payload.
type RecoveryReference struct {
	installation, mode, digest string
	sequence                   uint64
	catalog                    Catalog
}

func (r RecoveryReference) Installation() string { return r.installation }
func (r RecoveryReference) Mode() string         { return r.mode }
func (r RecoveryReference) Sequence() uint64     { return r.sequence }
func (r RecoveryReference) Digest() string       { return r.digest }
func (r RecoveryReference) Catalog() Catalog {
	return Catalog{r.catalog.revision, r.catalog.digest, r.catalog.Accounts()}
}

func DecodeRecoveryReference(data []byte) (RecoveryReference, error) {
	if len(data) == 0 || len(data) > ReferenceLimit || !utf8.Valid(data) {
		return RecoveryReference{}, InvalidReference
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	raw, e := readJSON(d, 0)
	if e != nil {
		return RecoveryReference{}, InvalidReference
	}
	if _, e = d.Token(); e != io.EOF {
		return RecoveryReference{}, InvalidReference
	}
	row, ok := object(raw, "schema", "installation", "sequence", "mode", "catalog")
	if !ok {
		return RecoveryReference{}, InvalidReference
	}
	schema, ok := positive(row["schema"], 1)
	if !ok || schema != 1 {
		return RecoveryReference{}, InvalidReference
	}
	var ref RecoveryReference
	if ref.installation, ok = matching(row["installation"], token); !ok {
		return ref, InvalidReference
	}
	if ref.sequence, ok = positive(row["sequence"], math.MaxInt64); !ok {
		return ref, InvalidReference
	}
	if ref.mode, ok = oneOf(row["mode"], "first-install", "blocked-recovery"); !ok {
		return ref, InvalidReference
	}
	catalog, e := json.Marshal(row["catalog"])
	if e != nil {
		return ref, InvalidReference
	}
	if ref.catalog, e = DecodeCatalog(catalog); e != nil {
		return ref, InvalidReference
	}
	if ref.mode == "first-install" {
		if ref.sequence != 1 || ref.catalog.Revision() != 1 {
			return ref, InvalidReference
		}
		for _, a := range ref.catalog.accounts {
			if a.Kind != "ordinary" || a.State != "active" || a.Barrier || a.Operation != "" || a.Revision != 1 {
				return ref, InvalidReference
			}
		}
	} else {
		if ref.sequence < 2 || ref.catalog.Revision() < 2 {
			return ref, InvalidReference
		}
		for _, a := range ref.catalog.accounts {
			if a.State != "tombstone" && (a.State != "recovering" || !a.Barrier) {
				return ref, InvalidReference
			}
		}
	}
	// One pending operation belongs to one association. Tombstones reserve it too.
	operations := map[string]bool{}
	for _, a := range ref.catalog.accounts {
		if a.Operation != "" {
			if operations[a.Operation] {
				return ref, InvalidReference
			}
			operations[a.Operation] = true
		}
	}
	canonical, e := json.Marshal(row)
	if e != nil {
		return ref, InvalidReference
	}
	sum := sha256.Sum256(canonical)
	ref.digest = hex.EncodeToString(sum[:])
	return ref, nil
}

// ReferenceSource must authenticate the installation, retain ALL ownership and
// tombstones, and supply its current statement across local rollback. A file in
// the same snapshot is insufficient. No production implementation exists yet.
type ReferenceSource interface {
	Snapshot(string) (RecoveryReference, error)
}
type RecoveryDisposition string

const (
	BootstrapRequired      RecoveryDisposition = "BootstrapRequired"
	BlockedRestoreRequired RecoveryDisposition = "BlockedRestoreRequired"
	ReferenceMatches       RecoveryDisposition = "ReferenceMatches"
)

type RecoveryAssessment struct {
	disposition RecoveryDisposition
	reference   RecoveryReference
	local       Catalog
}

func (a RecoveryAssessment) Disposition() RecoveryDisposition { return a.disposition }
func (a RecoveryAssessment) ReferenceDigest() string          { return a.reference.digest }
func (a RecoveryAssessment) Sequence() uint64                 { return a.reference.sequence }
func (a RecoveryAssessment) LocalDigest() string              { return a.local.digest }

// RecoveryInspector performs no authorization dialog, writes, plan issuance or
// admission. A successful assessment is diagnostic only. Capability is a
// separately supplied maintenance role; root/wheel have no implicit capability.
type RecoveryInspector struct {
	references      ReferenceSource
	local           Snapshotter
	resolveSender   func(string) (uint32, error)
	capable         func(uint32, string) (bool, error)
	observeIdentity func(uint32) (Identity, error)
}

func NewRecoveryInspector(references ReferenceSource, local Snapshotter, resolveSender func(string) (uint32, error), capable func(uint32, string) (bool, error), observeIdentity func(uint32) (Identity, error)) *RecoveryInspector {
	return &RecoveryInspector{references, local, resolveSender, capable, observeIdentity}
}
func (s *RecoveryInspector) caller(sender, installation string) (uint32, error) {
	if s == nil || !uniqueSender.MatchString(sender) || !token.MatchString(installation) || s.resolveSender == nil || s.capable == nil {
		return 0, Denied
	}
	uid, e := s.resolveSender(sender)
	if e != nil || uid == math.MaxUint32 {
		return 0, Denied
	}
	allowed, e := s.capable(uid, installation)
	if e != nil || !allowed {
		return 0, Denied
	}
	return uid, nil
}
func (s *RecoveryInspector) Inspect(sender, installation string) (RecoveryAssessment, error) {
	uid, e := s.caller(sender, installation)
	if e != nil {
		return RecoveryAssessment{}, e
	}
	if s.references == nil || s.local == nil {
		return RecoveryAssessment{}, RecoveryRequired
	}
	ref, e := s.references.Snapshot(installation)
	if e != nil || ref.installation != installation || !hash.MatchString(ref.digest) || ref.sequence == 0 {
		return RecoveryAssessment{}, RecoveryRequired
	}
	// Re-decode a constructed statement to reject a malformed trusted adapter.
	data, e := referenceBytes(ref)
	if e != nil {
		return RecoveryAssessment{}, InvalidReference
	}
	checked, e := DecodeRecoveryReference(data)
	if e != nil || checked.digest != ref.digest {
		return RecoveryAssessment{}, InvalidReference
	}
	local, localErr := s.local.Snapshot()
	assessment := RecoveryAssessment{reference: ref, local: local}
	if localErr != nil {
		// Unsafe ownership/permissions/lock/contention are not permission to repair.
		if localErr != RecoveryRequired && localErr != InvalidCatalog {
			return RecoveryAssessment{}, StorageFailure
		}
		if ref.mode == "first-install" {
			if localErr != RecoveryRequired {
				return RecoveryAssessment{}, Conflict
			}
			assessment.disposition = BootstrapRequired
		} else {
			assessment.disposition = BlockedRestoreRequired
		}
	} else {
		if local.revision == 0 || !hash.MatchString(local.digest) {
			return RecoveryAssessment{}, InvalidCatalog
		}
		if local.digest == ref.catalog.digest {
			assessment.disposition = ReferenceMatches
		} else {
			if ref.mode != "blocked-recovery" || ref.catalog.revision <= local.revision || !preservesOwnership(local, ref.catalog) {
				return RecoveryAssessment{}, Conflict
			}
			assessment.disposition = BlockedRestoreRequired
		}
	}
	if s.observeIdentity == nil {
		return RecoveryAssessment{}, InvalidIdentity
	}
	for _, a := range ref.catalog.accounts {
		// Historical tombstones need not have a live NSS account and never free IDs.
		if a.State == "tombstone" {
			continue
		}
		observed, e := s.observeIdentity(a.Identity.UID)
		if e != nil || observed != a.Identity {
			return RecoveryAssessment{}, InvalidIdentity
		}
	}
	latest, e := s.references.Snapshot(installation)
	if e != nil || latest.digest != ref.digest || latest.sequence != ref.sequence || latest.installation != installation {
		return RecoveryAssessment{}, Conflict
	}
	latestData, e := referenceBytes(latest)
	if e != nil {
		return RecoveryAssessment{}, Conflict
	}
	latestChecked, e := DecodeRecoveryReference(latestData)
	if e != nil || latestChecked.digest != ref.digest {
		return RecoveryAssessment{}, Conflict
	}
	after, e := s.caller(sender, installation)
	if e != nil || after != uid {
		return RecoveryAssessment{}, Denied
	}
	return assessment, nil
}
func referenceBytes(ref RecoveryReference) ([]byte, error) {
	data, _, e := encodeCatalog(ref.catalog)
	if e != nil {
		return nil, e
	}
	return json.Marshal(map[string]any{"schema": 1, "installation": ref.installation, "sequence": ref.sequence, "mode": ref.mode, "catalog": json.RawMessage(data)})
}
func preservesOwnership(before, after Catalog) bool {
	for _, old := range before.accounts {
		found := false
		for _, next := range after.accounts {
			if old.Association != next.Association {
				continue
			}
			found = true
			if old.Generation != next.Generation || old.Identity != next.Identity || old.Kind != next.Kind || old.Policy != next.Policy || !reflect.DeepEqual(old.Adapters, next.Adapters) || next.Revision < old.Revision {
				return false
			}
			if old.State != "tombstone" && next.State == "tombstone" {
				return false
			}
			if old.State == "tombstone" {
				if !reflect.DeepEqual(old, next) {
					return false
				}
			}
			if old.Operation != "" && old.Operation != next.Operation {
				return false
			}
		}
		if !found {
			return false
		}
	}
	return true
}
