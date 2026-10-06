package parental

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func referenceRow(mode string) map[string]any {
	c := fixtureRow()
	seq := 1
	if mode == "blocked-recovery" {
		seq = 2
		c["revision"] = 2
		first(c)["revision"] = 2
		first(c)["state"] = "recovering"
		first(c)["barrier"] = true
		first(c)["operation"] = strings.Repeat("e", 32)
	}
	return map[string]any{"schema": 1, "installation": strings.Repeat("1", 32), "sequence": seq, "mode": mode, "catalog": c}
}
func decodeReference(t *testing.T, row map[string]any) RecoveryReference {
	t.Helper()
	r, e := DecodeRecoveryReference(encodeRow(t, row))
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestRecoveryReferenceStrictSchemaAndModes(t *testing.T) {
	for _, mode := range []string{"first-install", "blocked-recovery"} {
		r := decodeReference(t, referenceRow(mode))
		data, e := referenceBytes(r)
		if e != nil {
			t.Fatal(e)
		}
		again, e := DecodeRecoveryReference(data)
		if e != nil || again.Digest() != r.Digest() {
			t.Fatal(e)
		}
		copied := r.Catalog()
		copied.accounts[0].Identity.User = "modified"
		if r.Catalog().Accounts()[0].Identity.User != "ordinaryuser" {
			t.Fatal("mutable reference")
		}
	}
	for _, mode := range []string{"unknown", "null", "bad-sequence", "unsupported", "first-later", "first-blocked", "first-supervised", "recovery-early", "recovery-active", "recovery-missing-barrier", "bad-catalog", "duplicate-op"} {
		t.Run(mode, func(t *testing.T) {
			row := referenceRow("first-install")
			switch mode {
			case "unknown":
				row["extra"] = true
			case "null":
				row["installation"] = nil
			case "bad-sequence":
				row["sequence"] = 1.5
			case "unsupported":
				row["mode"] = "restore-backup"
			case "first-later":
				row["sequence"] = 2
			case "first-blocked":
				first(row["catalog"].(map[string]any))["barrier"] = true
			case "first-supervised":
				a := first(row["catalog"].(map[string]any))
				a["kind"] = "supervised"
				a["policy"] = strings.Repeat("f", 64)
				a["adapters"] = map[string]any{"fixture": strings.Repeat("c", 64)}
			case "recovery-early":
				row = referenceRow("blocked-recovery")
				row["sequence"] = 1
			case "recovery-active":
				row = referenceRow("blocked-recovery")
				a := first(row["catalog"].(map[string]any))
				a["state"] = "active"
				a["operation"] = ""
				a["barrier"] = false
			case "recovery-missing-barrier":
				row = referenceRow("blocked-recovery")
				first(row["catalog"].(map[string]any))["barrier"] = false
			case "bad-catalog":
				row["catalog"] = map[string]any{}
			case "duplicate-op":
				row = referenceRow("blocked-recovery")
				c := row["catalog"].(map[string]any)
				a := map[string]any{}
				for k, v := range first(c) {
					a[k] = v
				}
				a["association"] = strings.Repeat("c", 32)
				a["generation"] = strings.Repeat("d", 32)
				a["identity"] = Identity{"peer", 1003, 100, "users"}
				c["accounts"] = append(c["accounts"].([]any), a)
			}
			_, e := DecodeRecoveryReference(encodeRow(t, row))
			assertCode(t, e, InvalidReference)
		})
	}
	valid := encodeRow(t, referenceRow("first-install"))
	for _, data := range [][]byte{nil, bytes.Repeat([]byte("x"), ReferenceLimit+1), []byte("\xff"), append(append([]byte{}, valid...), []byte("{}")...), bytes.Replace(valid, []byte(`"schema":1`), []byte(`"schema":1,"schema":1`), 1)} {
		_, e := DecodeRecoveryReference(data)
		assertCode(t, e, InvalidReference)
	}
}

type referenceSourceFixture struct {
	ref   RecoveryReference
	calls int
	err   error
	after func()
}

func (s *referenceSourceFixture) Snapshot(string) (RecoveryReference, error) {
	s.calls++
	if s.calls == 2 && s.after != nil {
		s.after()
	}
	return s.ref, s.err
}
func inspectorFixture(t *testing.T, mode string) (*RecoveryInspector, *referenceSourceFixture, *planSource, *bool) {
	t.Helper()
	refs := &referenceSourceFixture{ref: decodeReference(t, referenceRow(mode))}
	local := &planSource{err: RecoveryRequired}
	allowed := true
	s := NewRecoveryInspector(refs, local, func(string) (uint32, error) { return 1000, nil }, func(uint32, string) (bool, error) { return allowed, nil }, observe)
	return s, refs, local, &allowed
}
func inspect(t *testing.T, s *RecoveryInspector) (RecoveryAssessment, error) {
	t.Helper()
	return s.Inspect(":1.20", strings.Repeat("1", 32))
}
func TestRecoveryInspectionNeverCreatesAuthority(t *testing.T) {
	for _, mode := range []string{"first-install", "blocked-recovery"} {
		s, refs, local, _ := inspectorFixture(t, mode)
		a, e := inspect(t, s)
		want := BootstrapRequired
		if mode == "blocked-recovery" {
			want = BlockedRestoreRequired
		}
		if e != nil || a.Disposition() != want || a.ReferenceDigest() != refs.ref.Digest() || a.Sequence() != refs.ref.Sequence() || a.LocalDigest() != "" {
			t.Fatal(a, e)
		}
		// Real missing canonical state remains absent; inspection is read-only.
		w, _, dir := writerFixture(t)
		if e := removeCatalog(dir); e != nil {
			t.Fatal(e)
		}
		r := NewReader(dir)
		r.owner = w.owner
		s.local = r
		_, e = inspect(t, s)
		if e != nil {
			t.Fatal(e)
		}
		if e := catalogAbsent(dir); e != nil {
			t.Fatal(e)
		}
		s.local = local
		local.catalog = refs.ref.Catalog()
		local.err = nil
		a, e = inspect(t, s)
		if e != nil || a.Disposition() != ReferenceMatches {
			t.Fatal(a, e)
		}
	}
}
func TestRecoveryInspectionRejectsBeforeTargetLookup(t *testing.T) {
	s, refs, local, allowed := inspectorFixture(t, "first-install")
	*allowed = false
	_, e := inspect(t, s)
	assertCode(t, e, Denied)
	if refs.calls != 0 || local.reads != 0 {
		t.Fatal("lookup before capability")
	}
	s.resolveSender = func(string) (uint32, error) { return 0, nil }
	_, e = inspect(t, s)
	assertCode(t, e, Denied)
	for _, mode := range []string{"missing-source", "bad-source", "wrong-installation", "corrupt-first", "unsafe", "io", "changed-reference", "revoked", "changed-uid", "identity"} {
		t.Run(mode, func(t *testing.T) {
			s, refs, local, allowed := inspectorFixture(t, "first-install")
			want := RecoveryRequired
			switch mode {
			case "missing-source":
				s.references = nil
			case "bad-source":
				refs.err = errors.New("private reference path")
			case "wrong-installation":
				refs.ref.installation = strings.Repeat("2", 32)
			case "corrupt-first":
				local.err = InvalidCatalog
				want = Conflict
			case "unsafe":
				local.err = UnsafeCatalog
				want = StorageFailure
			case "io":
				local.err = errors.New("private file path")
				want = StorageFailure
			case "changed-reference":
				refs.after = func() { refs.ref = decodeReference(t, referenceRow("blocked-recovery")) }
				want = Conflict
			case "revoked":
				refs.after = func() { *allowed = false }
				want = Denied
			case "changed-uid":
				calls := 0
				s.resolveSender = func(string) (uint32, error) { calls++; return uint32(999 + calls), nil }
				want = Denied
			case "identity":
				s.observeIdentity = func(uint32) (Identity, error) { return Identity{}, nil }
				want = InvalidIdentity
			}
			_, e := inspect(t, s)
			assertCode(t, e, want)
		})
	}
}
func TestBlockedRecoveryPreservesKnownOwnership(t *testing.T) {
	for _, mode := range []string{"preserve", "delete", "identity", "generation", "class", "policy", "tombstone", "new-tombstone", "pending-op", "old-revision"} {
		t.Run(mode, func(t *testing.T) {
			s, refs, local, _ := inspectorFixture(t, "blocked-recovery")
			local.err = nil
			local.catalog = decodeRow(t, fixtureRow())
			row := referenceRow("blocked-recovery")
			candidate := row["catalog"].(map[string]any)
			account := first(candidate)
			switch mode {
			case "delete":
				candidate["accounts"] = []any{}
			case "identity":
				account["identity"] = Identity{"replacement", 1002, 100, "users"}
			case "generation":
				account["generation"] = strings.Repeat("c", 32)
			case "class":
				account["kind"] = "supervised"
				account["policy"] = strings.Repeat("f", 64)
				account["adapters"] = map[string]any{"fixture": strings.Repeat("c", 64)}
			case "policy":
				before := local.catalog
				before.accounts[0].Kind = "supervised"
				before.accounts[0].Policy = strings.Repeat("d", 64)
				before.accounts[0].Adapters = []Adapter{{"fixture", strings.Repeat("c", 64)}}
				_, local.catalog, _ = encodeCatalog(before)
				account["kind"] = "supervised"
				account["policy"] = strings.Repeat("f", 64)
				account["adapters"] = map[string]any{"fixture": strings.Repeat("c", 64)}
			case "tombstone":
				before := local.catalog
				before.accounts[0].State = "tombstone"
				before.accounts[0].Operation = strings.Repeat("e", 32)
				_, local.catalog, _ = encodeCatalog(before)
			case "new-tombstone":
				account["state"] = "tombstone"
			case "pending-op":
				before := local.catalog
				before.accounts[0].State = "recovering"
				before.accounts[0].Barrier = true
				before.accounts[0].Operation = strings.Repeat("f", 32)
				_, local.catalog, _ = encodeCatalog(before)
			case "old-revision":
				before := local.catalog
				before.revision = 3
				_, local.catalog, _ = encodeCatalog(before)
			}
			refs.ref = decodeReference(t, row)
			a, e := inspect(t, s)
			if mode == "preserve" {
				if e != nil || a.Disposition() != BlockedRestoreRequired {
					t.Fatal(a, e)
				}
			} else {
				assertCode(t, e, Conflict)
			}
		})
	}
	s, refs, local, _ := inspectorFixture(t, "blocked-recovery")
	local.err = InvalidCatalog
	a, e := inspect(t, s)
	if e != nil || a.Disposition() != BlockedRestoreRequired {
		t.Fatal(a, e)
	}
	if !reflect.DeepEqual(a.reference.Catalog().Accounts(), refs.ref.Catalog().Accounts()) {
		t.Fatal("reference changed")
	}
}

func removeCatalog(dir string) error { return os.Remove(filepath.Join(dir, "catalog.json")) }
func catalogAbsent(dir string) error {
	_, e := os.Lstat(filepath.Join(dir, "catalog.json"))
	if os.IsNotExist(e) {
		return nil
	}
	return errors.New("catalog unexpectedly exists")
}
