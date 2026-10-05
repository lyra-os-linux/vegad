package parental

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

type planSource struct {
	catalog Catalog
	err     error
	reads   int
}

func (s *planSource) Snapshot() (Catalog, error) { s.reads++; return s.catalog, s.err }

type planFixture struct {
	p      *Planner
	source *planSource
	uid    uint32
	allow  bool
	now    time.Duration
	calls  []string
}

func newPlanFixture(t *testing.T) *planFixture {
	t.Helper()
	f := &planFixture{source: &planSource{catalog: decodeRow(t, fixtureRow())}, uid: 1000, allow: true}
	f.p = NewPlanner(f.source, func(sender string) (uint32, error) { f.calls = append(f.calls, "sender"); return f.uid, nil }, func(uid uint32, association string) (bool, error) {
		f.calls = append(f.calls, "capability")
		return f.allow, nil
	}, func(uid uint32) (Identity, error) { f.calls = append(f.calls, "identity"); return observe(uid) })
	f.p.clock = func() time.Duration { return f.now }
	return f
}
func (f *planFixture) prepare(t *testing.T) Plan {
	t.Helper()
	p, err := f.p.Prepare(":1.20", strings.Repeat("a", 32), 1)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestPlanServerOwnedBindings(t *testing.T) {
	f := newPlanFixture(t)
	plan := f.prepare(t)
	if !token.MatchString(plan.Identifier()) || plan.Association() != strings.Repeat("a", 32) || plan.Generation() != strings.Repeat("b", 32) || plan.Revision() != 1 || plan.CatalogDigest() != f.source.catalog.Digest() || plan.Effect() != "deny-new-admission-only" {
		t.Fatal("incorrect plan bindings")
	}
	a := plan.Account()
	a.Identity.User = "redirected"
	a.Association = strings.Repeat("f", 32)
	validated, err := f.p.Validate(":1.20", plan.Identifier())
	if err != nil {
		t.Fatal(err)
	}
	if validated.Account().Identity.User != "ordinaryuser" || validated.Association() != plan.Association() {
		t.Fatal("client accessor redirected stored plan")
	}
	// Validation is read-only and is not consume/execute/interactive authority.
	if _, err = f.p.Validate(":1.20", plan.Identifier()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls[:2], ","); got != "sender,capability" {
		t.Fatal("target lookup preceded capability", got)
	}
}
func TestPrepareDeniedBeforeTargetLookup(t *testing.T) {
	for _, association := range []string{strings.Repeat("a", 32), strings.Repeat("f", 32)} {
		t.Run(association, func(t *testing.T) {
			f := newPlanFixture(t)
			f.allow = false
			_, err := f.p.Prepare(":1.20", association, 1)
			assertCode(t, err, Denied)
			if f.source.reads != 0 || len(f.calls) != 2 {
				t.Fatal("denial exposed target lookup")
			}
		})
	}
	for name, setup := range map[string]func(*planFixture){
		"missing resolver": func(f *planFixture) { f.p.resolveSender = nil },
		"resolver error": func(f *planFixture) {
			f.p.resolveSender = func(string) (uint32, error) { return 0, errors.New("private transport details") }
		},
		"reserved uid":       func(f *planFixture) { f.uid = math.MaxUint32 },
		"missing capability": func(f *planFixture) { f.p.capable = nil },
		"capability error": func(f *planFixture) {
			f.p.capable = func(uint32, string) (bool, error) { return true, errors.New("private role details") }
		},
		"root without capability": func(f *planFixture) { f.uid = 0; f.allow = false },
	} {
		t.Run(name, func(t *testing.T) {
			f := newPlanFixture(t)
			setup(f)
			_, err := f.p.Prepare(":1.20", strings.Repeat("a", 32), 1)
			assertCode(t, err, Denied)
			if f.source.reads != 0 {
				t.Fatal("looked up target")
			}
		})
	}
	f := newPlanFixture(t)
	f.uid = 0 // only explicit trusted callback grants technical capability
	if _, err := f.p.Prepare(":1.20", strings.Repeat("a", 32), 1); err != nil {
		t.Fatal(err)
	}
}
func TestPrepareInvalidTransportOrAssociation(t *testing.T) {
	for _, sender := range []string{"", "org.lyraos.Vega1", ":1", "1.20", ":1.20\n", ":-1.20"} {
		f := newPlanFixture(t)
		_, err := f.p.Prepare(sender, strings.Repeat("a", 32), 1)
		assertCode(t, err, Denied)
		if len(f.calls) != 0 || f.source.reads != 0 {
			t.Fatal("invalid sender reached callbacks")
		}
	}
	f := newPlanFixture(t)
	_, err := f.p.Prepare(":1.20", "invalid", 1)
	assertCode(t, err, Denied)
	if len(f.calls) != 0 || f.source.reads != 0 {
		t.Fatal("invalid association reached callbacks")
	}
	var p *Planner
	_, err = p.Prepare(":1.20", strings.Repeat("a", 32), 1)
	assertCode(t, err, Denied)
}
func TestPrepareRefusesUnsupportedOrStaleAccount(t *testing.T) {
	f := newPlanFixture(t)
	for _, revision := range []uint64{0, 2, math.MaxUint64} {
		_, err := f.p.Prepare(":1.20", strings.Repeat("a", 32), revision)
		assertCode(t, err, Conflict)
	}
	_, err := f.p.Prepare(":1.20", strings.Repeat("f", 32), 1)
	assertCode(t, err, RecoveryRequired)
	for _, kind := range []string{"supervised", "blocked", "changed identity"} {
		t.Run(kind, func(t *testing.T) {
			f := newPlanFixture(t)
			switch kind {
			case "supervised":
				f.source.catalog = decodeRow(t, supervisedRow())
			case "blocked":
				r := fixtureRow()
				first(r)["barrier"] = true
				first(r)["operation"] = strings.Repeat("c", 32)
				f.source.catalog = decodeRow(t, r)
			case "changed identity":
				f.p.observeIdentity = func(uid uint32) (Identity, error) { i, _ := observe(uid); i.User = "peer"; return i, nil }
			}
			_, err := f.p.Prepare(":1.20", strings.Repeat("a", 32), 1)
			expected := UnsupportedOperation
			if kind == "blocked" {
				expected = Blocked
			}
			if kind == "changed identity" {
				expected = InvalidIdentity
			}
			assertCode(t, err, expected)
			if len(f.p.plans) != 0 {
				t.Fatal("invalid account got a plan")
			}
		})
	}
}
func TestPrepareRechecksCallerAfterObservations(t *testing.T) {
	for _, changeUID := range []bool{false, true} {
		f := newPlanFixture(t)
		f.p.observeIdentity = func(uid uint32) (Identity, error) {
			if changeUID {
				f.uid++
			} else {
				f.allow = false
			}
			return observe(uid)
		}
		_, err := f.p.Prepare(":1.20", strings.Repeat("a", 32), 1)
		assertCode(t, err, Denied)
		if len(f.p.plans) != 0 {
			t.Fatal("revoked caller received a plan")
		}
	}
}
func TestValidateSenderAndRestart(t *testing.T) {
	f := newPlanFixture(t)
	p := f.prepare(t)
	reads := f.source.reads
	f.calls = nil
	for _, id := range []string{"invalid", strings.Repeat("f", 32)} {
		_, err := f.p.Validate(":1.20", id)
		assertCode(t, err, Denied)
	}
	_, err := f.p.Validate(":1.21", p.Identifier())
	assertCode(t, err, Denied)
	if len(f.calls) != 0 || f.source.reads != reads {
		t.Fatal("unowned token reached target")
	}
	fresh := newPlanFixture(t)
	_, err = fresh.p.Validate(":1.20", p.Identifier())
	assertCode(t, err, Denied)
	var nilPlanner *Planner
	_, err = nilPlanner.Validate(":1.20", p.Identifier())
	assertCode(t, err, Denied)
}
func TestValidateRevocationUIDAndIdentity(t *testing.T) {
	for name, change := range map[string]func(*planFixture){
		"revoked":      func(f *planFixture) { f.allow = false },
		"uid replaced": func(f *planFixture) { f.uid++ },
		"identity reused": func(f *planFixture) {
			f.p.observeIdentity = func(uid uint32) (Identity, error) { i, _ := observe(uid); i.GID++; return i, nil }
		},
		"NSS unavailable": func(f *planFixture) {
			f.p.observeIdentity = func(uint32) (Identity, error) { return Identity{}, errors.New("private NSS path") }
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPlanFixture(t)
			p := f.prepare(t)
			change(f)
			_, err := f.p.Validate(":1.20", p.Identifier())
			code := Denied
			if name == "identity reused" || name == "NSS unavailable" {
				code = InvalidIdentity
			}
			assertCode(t, err, code)
		})
	}
}
func TestValidateCatalogChanges(t *testing.T) {
	changes := map[string]func(map[string]any){
		"global revision":  func(r map[string]any) { r["revision"] = 2 },
		"account revision": func(r map[string]any) { r["revision"] = 2; first(r)["revision"] = 2 },
		"generation":       func(r map[string]any) { first(r)["generation"] = strings.Repeat("c", 32) },
		"association":      func(r map[string]any) { first(r)["association"] = strings.Repeat("c", 32) },
		"new barrier":      func(r map[string]any) { first(r)["barrier"] = true; first(r)["operation"] = strings.Repeat("c", 32) },
		"missing target":   func(r map[string]any) { r["accounts"] = []any{} },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			f := newPlanFixture(t)
			p := f.prepare(t)
			r := fixtureRow()
			change(r)
			f.source.catalog = decodeRow(t, r)
			_, err := f.p.Validate(":1.20", p.Identifier())
			assertCode(t, err, Conflict)
		})
	}
}
func TestValidateRechecksCallerAndDeadlineAfterIdentity(t *testing.T) {
	for _, mode := range []string{"revoked", "uid", "expired"} {
		t.Run(mode, func(t *testing.T) {
			f := newPlanFixture(t)
			p := f.prepare(t)
			f.p.observeIdentity = func(uid uint32) (Identity, error) {
				switch mode {
				case "revoked":
					f.allow = false
				case "uid":
					f.uid++
				case "expired":
					f.now = planTTL
				}
				return observe(uid)
			}
			_, err := f.p.Validate(":1.20", p.Identifier())
			code := Denied
			if mode == "expired" {
				code = Conflict
			}
			assertCode(t, err, code)
		})
	}
}
func TestPlannerExpiryAndCapacity(t *testing.T) {
	f := newPlanFixture(t)
	first := f.prepare(t)
	f.now = planTTL - time.Nanosecond
	if _, err := f.p.Validate(":1.20", first.Identifier()); err != nil {
		t.Fatal(err)
	}
	f.now = planTTL
	_, err := f.p.Validate(":1.20", first.Identifier())
	assertCode(t, err, Conflict)
	if _, err = f.p.Prepare(":1.20", strings.Repeat("a", 32), 1); err != nil {
		t.Fatal(err)
	}
	if len(f.p.plans) != 1 {
		t.Fatal("expired plans not pruned")
	}
	f = newPlanFixture(t)
	for n := 0; n < maxPlans; n++ {
		f.prepare(t)
	}
	_, err = f.p.Prepare(":1.20", strings.Repeat("a", 32), 1)
	assertCode(t, err, Busy)
	if len(f.p.plans) != maxPlans {
		t.Fatal("plan limit exceeded")
	}
	f.now = planTTL
	f.prepare(t)
	if len(f.p.plans) != 1 {
		t.Fatal("capacity not released on expiry")
	}
}
func TestPlannerClockFailures(t *testing.T) {
	for _, now := range []time.Duration{-1, time.Duration(math.MaxInt64)} {
		f := newPlanFixture(t)
		f.now = now
		_, err := f.p.Prepare(":1.20", strings.Repeat("a", 32), 1)
		assertCode(t, err, InvalidClock)
	}
	f := newPlanFixture(t)
	f.now = time.Second
	p := f.prepare(t)
	f.now = 0
	_, err := f.p.Validate(":1.20", p.Identifier())
	assertCode(t, err, InvalidClock)
	f.now = 2 * time.Second
	if _, err = f.p.Validate(":1.20", p.Identifier()); err != nil {
		t.Fatal(err)
	}
}
func TestPlannerEntropyFailureDoesNotOverwrite(t *testing.T) {
	f := newPlanFixture(t)
	f.p.identifier = func() (string, error) { return "", errors.New("entropy failure details") }
	_, err := f.p.Prepare(":1.20", strings.Repeat("a", 32), 1)
	assertCode(t, err, StorageFailure)
	f.p.identifier = func() (string, error) { return "invalid", nil }
	_, err = f.p.Prepare(":1.20", strings.Repeat("a", 32), 1)
	assertCode(t, err, StorageFailure)
	f.p.identifier = func() (string, error) { return strings.Repeat("d", 32), nil }
	p := f.prepare(t)
	_, err = f.p.Prepare(":1.20", strings.Repeat("a", 32), 1)
	assertCode(t, err, Busy)
	if len(f.p.plans) != 1 {
		t.Fatal("collision changed map")
	}
	if _, err = f.p.Validate(":1.20", p.Identifier()); err != nil {
		t.Fatal("collision overwrote existing plan", err)
	}
}
func TestPlannerStorageErrorsAreSanitized(t *testing.T) {
	for _, stage := range []string{"prepare", "validate"} {
		for _, code := range []Code{InvalidCatalog, UnsafeCatalog, RecoveryRequired, Busy, StorageFailure} {
			f := newPlanFixture(t)
			var p Plan
			if stage == "validate" {
				p = f.prepare(t)
			}
			if code == StorageFailure {
				f.source.err = errors.New("private catalog path")
			} else {
				f.source.err = fmt.Errorf("private path: %w", code)
			}
			var err error
			if stage == "prepare" {
				_, err = f.p.Prepare(":1.20", strings.Repeat("a", 32), 1)
			} else {
				_, err = f.p.Validate(":1.20", p.Identifier())
			}
			assertCode(t, err, code)
			if err.Error() != string(code) {
				t.Fatal("error exposed private data")
			}
		}
	}
	f := newPlanFixture(t)
	f.p.source = nil
	_, err := f.p.Prepare(":1.20", strings.Repeat("a", 32), 1)
	assertCode(t, err, RecoveryRequired)
	f = newPlanFixture(t)
	f.source.catalog = Catalog{}
	_, err = f.p.Prepare(":1.20", strings.Repeat("a", 32), 1)
	assertCode(t, err, InvalidCatalog)
}
func TestPlannerConcurrentPrepareAndValidate(t *testing.T) {
	// Immutable storage/callbacks: the planner owns only its map and clock lock.
	c := decodeRow(t, fixtureRow())
	p := NewPlanner(staticCatalog{c}, func(string) (uint32, error) { return 1000, nil }, func(uint32, string) (bool, error) { return true, nil }, observe)
	ids := make(chan string, 64)
	failures := make(chan error, 64)
	var group sync.WaitGroup
	for n := 0; n < 64; n++ {
		group.Add(1)
		go func() {
			defer group.Done()
			plan, err := p.Prepare(":1.20", strings.Repeat("a", 32), 1)
			if err != nil {
				failures <- err
				return
			}
			if _, err = p.Validate(":1.20", plan.Identifier()); err != nil {
				failures <- err
				return
			}
			ids <- plan.Identifier()
		}()
	}
	group.Wait()
	close(ids)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatal("duplicate identifier")
		}
		seen[id] = true
	}
	if len(seen) != 64 {
		t.Fatal("lost concurrent plan")
	}
}

type staticCatalog struct{ c Catalog }

func (s staticCatalog) Snapshot() (Catalog, error) { return s.c, nil }
