package parental

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

type testLeases struct {
	state     MutationState
	held      bool
	exclusive bool
	calls     int
	before    func(bool)
}

func (l *testLeases) WithLease(plan Plan, exclusive bool, visit func(MutationState) error) error {
	if l.held {
		return Busy
	}
	if l.before != nil {
		l.before(exclusive)
	}
	l.held = true
	l.exclusive = exclusive
	l.calls++
	defer func() { l.held = false; l.exclusive = false }()
	return visit(l.state)
}

type guardFixture struct {
	f              *planFixture
	g              *Guard
	leases         *testLeases
	groups         map[string]uint32
	effects        int
	authorizations int
}

func newGuardFixture(t *testing.T) *guardFixture {
	t.Helper()
	f := newPlanFixture(t)
	identity, _ := observe(1002)
	l := &testLeases{state: MutationState{Catalog: f.source.catalog, InventoryDigest: strings.Repeat("c", 64), LedgerDigest: strings.Repeat("d", 64), Proof: &identity}}
	out := &guardFixture{f: f, leases: l, groups: map[string]uint32{"users": 100, "wheel": 10}}
	out.g = NewGuard(f.p, l, func(name string) (uint32, error) {
		gid, ok := out.groups[name]
		if !ok {
			return 0, errors.New("unknown private group")
		}
		return gid, nil
	})
	return out
}
func (x *guardFixture) prepare(t *testing.T, input Mutation) AdministrativePlan {
	t.Helper()
	p, err := x.g.Prepare(":1.20", strings.Repeat("a", 32), 1, input)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func (x *guardFixture) auth(t *testing.T) func(AuthorizationRequest) (bool, error) {
	return func(request AuthorizationRequest) (bool, error) {
		t.Helper()
		x.authorizations++
		if x.leases.held {
			t.Fatal("authorization held a storage lease")
		}
		return true, nil
	}
}
func (x *guardFixture) effect(t *testing.T) func(Method, Account, Parameters) error {
	return func(method Method, a Account, p Parameters) error {
		t.Helper()
		if !x.leases.held || !x.leases.exclusive {
			t.Fatal("effect missing exclusive lease")
		}
		x.effects++
		return nil
	}
}
func grantInput() Mutation { return Mutation{Method: SetAdmin, IsAdmin: true} }
func TestGuardSealsParametersAndConsumesBeforeEffect(t *testing.T) {
	x := newGuardFixture(t)
	input := Mutation{Method: UpdateUser, FullName: "Lyra fixture $(literal)", Groups: []string{"users"}}
	p := x.prepare(t, input)
	input.FullName = "changed"
	input.Groups[0] = "wheel"
	input.IsAdmin = true
	public := p.Parameters()
	public.groups[0] = Group{"wheel", 10}
	public.fullName = "modified"
	authorize := func(request AuthorizationRequest) (bool, error) {
		x.auth(t)(request)
		if request.Method != UpdateUser || request.Association != strings.Repeat("a", 32) || request.Generation != strings.Repeat("b", 32) || request.Revision != 1 || request.Target.UID != 1002 || request.ParameterDigest != p.ParameterDigest() {
			t.Fatal("authorization was redirected")
		}
		request.Method = SetAdmin
		request.Parameters.groups[0] = Group{"wheel", 10}
		request.Parameters.fullName = "changed during authorization"
		return true, nil
	}
	effect := func(method Method, a Account, params Parameters) error {
		x.effect(t)(method, a, params)
		if method != UpdateUser || a.Identity.User != "ordinaryuser" || params.FullName() != "Lyra fixture $(literal)" || params.IsAdmin() || len(params.Groups()) != 1 || params.Groups()[0] != (Group{"users", 100}) {
			t.Fatal("sealed dispatch changed")
		}
		if _, err := x.f.p.Validate(":1.20", p.Identifier()); err != Denied {
			t.Fatal("plan was not consumed before adapter", err)
		}
		return nil
	}
	result, err := x.g.Execute(":1.20", p.Identifier(), authorize, effect)
	if err != nil || result != "OrdinaryMutationDispatched" || x.effects != 1 || x.authorizations != 1 {
		t.Fatal(result, err)
	}
	_, err = x.g.Execute(":1.20", p.Identifier(), x.auth(t), x.effect(t))
	assertCode(t, err, Denied)
	if x.effects != 1 || x.authorizations != 1 || x.leases.held {
		t.Fatal("replay or lease leak")
	}
}
func TestGuardChecksCapabilityBeforeGroupLookup(t *testing.T) {
	x := newGuardFixture(t)
	x.f.allow = false
	called := false
	x.g.resolveGroup = func(string) (uint32, error) { called = true; return 10, nil }
	_, err := x.g.Prepare(":1.20", strings.Repeat("a", 32), 1, grantInput())
	assertCode(t, err, Denied)
	if called || x.leases.calls != 0 || x.f.source.reads != 0 {
		t.Fatal("denial leaked target or group lookup")
	}
}
func TestGuardPrepareNegativeStateDropsBothPlans(t *testing.T) {
	cases := map[string]func(*guardFixture){
		"barrier":           func(x *guardFixture) { x.leases.state.Barrier = true },
		"journal":           func(x *guardFixture) { x.leases.state.Journal = true },
		"owner":             func(x *guardFixture) { x.leases.state.Owner = true },
		"missing proof":     func(x *guardFixture) { x.leases.state.Proof = nil },
		"wrong proof":       func(x *guardFixture) { id := *x.leases.state.Proof; id.UID++; x.leases.state.Proof = &id },
		"inventory invalid": func(x *guardFixture) { x.leases.state.InventoryDigest = "" },
		"ledger invalid":    func(x *guardFixture) { x.leases.state.LedgerDigest = "bad" },
		"catalog changed":   func(x *guardFixture) { r := fixtureRow(); r["revision"] = 2; x.leases.state.Catalog = decodeRow(t, r) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			x := newGuardFixture(t)
			change(x)
			_, err := x.g.Prepare(":1.20", strings.Repeat("a", 32), 1, grantInput())
			want := Blocked
			switch name {
			case "missing proof":
				want = RecoveryRequired
			case "wrong proof":
				want = InvalidIdentity
			case "inventory invalid", "ledger invalid":
				want = StorageFailure
			case "catalog changed":
				want = Conflict
			}
			assertCode(t, err, want)
			if len(x.g.plans) != 0 || len(x.f.p.plans) != 0 || x.leases.held {
				t.Fatal("failed preparation leaked plan or lease")
			}
		})
	}
}
func TestGuardChangesDuringAuthorizationBlockEffect(t *testing.T) {
	changes := map[string]func(*guardFixture){
		"barrier":       func(x *guardFixture) { x.leases.state.Barrier = true },
		"journal":       func(x *guardFixture) { x.leases.state.Journal = true },
		"owner":         func(x *guardFixture) { x.leases.state.Owner = true },
		"proof removed": func(x *guardFixture) { x.leases.state.Proof = nil },
		"inventory":     func(x *guardFixture) { x.leases.state.InventoryDigest = strings.Repeat("e", 64) },
		"ledger":        func(x *guardFixture) { x.leases.state.LedgerDigest = strings.Repeat("e", 64) },
		"group reused":  func(x *guardFixture) { x.groups["wheel"]++ },
		"capability":    func(x *guardFixture) { x.f.allow = false },
		"expired":       func(x *guardFixture) { x.f.now = planTTL },
		"catalog": func(x *guardFixture) {
			r := fixtureRow()
			r["revision"] = 2
			x.f.source.catalog = decodeRow(t, r)
			x.leases.state.Catalog = x.f.source.catalog
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			x := newGuardFixture(t)
			p := x.prepare(t, grantInput())
			_, err := x.g.Execute(":1.20", p.Identifier(), func(r AuthorizationRequest) (bool, error) { x.auth(t)(r); change(x); return true, nil }, x.effect(t))
			want := Conflict
			switch name {
			case "barrier", "journal", "owner":
				want = Blocked
			case "proof removed":
				want = RecoveryRequired
			case "capability":
				want = Denied
			}
			assertCode(t, err, want)
			if x.effects != 0 || x.authorizations != 1 || x.leases.held {
				t.Fatal("stale dispatch or lease leak")
			}
		})
	}
}
func TestGuardFinalLeaseRechecksStateAndDeadline(t *testing.T) {
	for _, phase := range []string{"barrier", "expiry", "group", "capability"} {
		t.Run(phase, func(t *testing.T) {
			x := newGuardFixture(t)
			p := x.prepare(t, grantInput())
			x.leases.before = func(exclusive bool) {
				if exclusive {
					switch phase {
					case "barrier":
						x.leases.state.Barrier = true
					case "expiry":
						x.f.now = planTTL
					case "group":
						x.groups["wheel"]++
					case "capability":
						x.f.allow = false
					}
				}
			}
			_, err := x.g.Execute(":1.20", p.Identifier(), x.auth(t), x.effect(t))
			want := Conflict
			if phase == "barrier" {
				want = Blocked
			}
			if phase == "capability" {
				want = Denied
			}
			assertCode(t, err, want)
			if x.effects != 0 || x.leases.held {
				t.Fatal("state changed on acquisition but dispatched")
			}
		})
	}
}
func TestGuardDenialAndCancellationPreservePlanForFreshAuthorization(t *testing.T) {
	for _, failure := range []bool{false, true} {
		x := newGuardFixture(t)
		p := x.prepare(t, grantInput())
		_, err := x.g.Execute(":1.20", p.Identifier(), func(r AuthorizationRequest) (bool, error) {
			x.auth(t)(r)
			if failure {
				return true, errors.New("private auth failure")
			}
			return false, nil
		}, x.effect(t))
		want := Denied
		if failure {
			want = AuthorizationFailure
		}
		assertCode(t, err, want)
		if x.effects != 0 || x.leases.held {
			t.Fatal("denied effect or lock leak")
		}
		if _, err = x.g.Execute(":1.20", p.Identifier(), x.auth(t), x.effect(t)); err != nil {
			t.Fatal("could not retry with fresh authorization", err)
		}
		if x.authorizations != 2 || x.effects != 1 {
			t.Fatal("retry skipped authorization")
		}
	}
}
func TestGuardAdapterFailureAndIdentityChangeConsumePlan(t *testing.T) {
	for _, failure := range []string{"adapter", "identity"} {
		t.Run(failure, func(t *testing.T) {
			x := newGuardFixture(t)
			p := x.prepare(t, grantInput())
			_, err := x.g.Execute(":1.20", p.Identifier(), x.auth(t), func(m Method, a Account, params Parameters) error {
				x.effect(t)(m, a, params)
				if failure == "adapter" {
					return errors.New("partial Unix write with private data")
				}
				x.f.p.observeIdentity = func(uid uint32) (Identity, error) { id, _ := observe(uid); id.User = "changed"; return id, nil }
				return nil
			})
			want := MutationFailure
			if failure == "identity" {
				want = InvalidIdentity
			}
			assertCode(t, err, want)
			_, err = x.g.Execute(":1.20", p.Identifier(), x.auth(t), x.effect(t))
			assertCode(t, err, Denied)
			if x.effects != 1 || x.authorizations != 1 || x.leases.held {
				t.Fatal("failed effect replayed or lock leaked")
			}
		})
	}
}
func TestGuardReentrancyAndCrossGuardReservation(t *testing.T) {
	x := newGuardFixture(t)
	p := x.prepare(t, grantInput())
	other := NewGuard(x.f.p, x.leases, x.g.resolveGroup)
	peer, err := other.Prepare(":1.20", strings.Repeat("a", 32), 1, grantInput())
	if err != nil {
		t.Fatal(err)
	}
	_, err = x.g.Execute(":1.20", p.Identifier(), func(r AuthorizationRequest) (bool, error) {
		x.auth(t)(r)
		_, err := x.g.Execute(":1.20", p.Identifier(), x.auth(t), x.effect(t))
		assertCode(t, err, Busy)
		_, err = other.Execute(":1.20", peer.Identifier(), x.auth(t), x.effect(t))
		assertCode(t, err, Busy)
		return true, nil
	}, x.effect(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.Execute(":1.20", peer.Identifier(), x.auth(t), x.effect(t)); err != nil {
		t.Fatal("reservation leaked", err)
	}
	if x.effects != 2 || x.authorizations != 2 {
		t.Fatal("reentrant authorization or dispatch")
	}
}
func TestGuardConcurrentSameTokenHasOneEffect(t *testing.T) {
	// Only the first call reaches leases/effect; other goroutines use immutable
	// callbacks. Channels keep the first authorization open until peers finish.
	catalog := decodeRow(t, fixtureRow())
	p := NewPlanner(staticCatalog{catalog}, func(string) (uint32, error) { return 1000, nil }, func(uint32, string) (bool, error) { return true, nil }, observe)
	id, _ := observe(1002)
	leases := &testLeases{state: MutationState{Catalog: catalog, InventoryDigest: strings.Repeat("c", 64), LedgerDigest: strings.Repeat("d", 64), Proof: &id}}
	g := NewGuard(p, leases, func(string) (uint32, error) { return 10, nil })
	planned, err := g.Prepare(":1.20", strings.Repeat("a", 32), 1, grantInput())
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	effects := 0
	go func() {
		_, err := g.Execute(":1.20", planned.Identifier(), func(AuthorizationRequest) (bool, error) { close(entered); <-release; return true, nil }, func(Method, Account, Parameters) error { effects++; return nil })
		done <- err
	}()
	<-entered
	var group sync.WaitGroup
	errs := make(chan error, 16)
	for n := 0; n < 16; n++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := g.Execute(":1.20", planned.Identifier(), func(AuthorizationRequest) (bool, error) { return true, nil }, func(Method, Account, Parameters) error { panic("duplicate effect") })
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		assertCode(t, err, Busy)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if effects != 1 || leases.held {
		t.Fatal("duplicate effect or lease leak")
	}
}

type leaseFunc func(Plan, bool, func(MutationState) error) error

func (f leaseFunc) WithLease(p Plan, exclusive bool, visit func(MutationState) error) error {
	return f(p, exclusive, visit)
}
func TestGuardRejectsBrokenLeaseBackend(t *testing.T) {
	for _, mode := range []string{"no visit", "double visit", "swallow rejection", "cleanup error"} {
		t.Run(mode, func(t *testing.T) {
			x := newGuardFixture(t)
			x.g.leases = leaseFunc(func(p Plan, exclusive bool, visit func(MutationState) error) error {
				switch mode {
				case "no visit":
					return nil
				case "double visit":
					_ = visit(x.leases.state)
					return visit(x.leases.state)
				case "swallow rejection":
					state := x.leases.state
					state.Barrier = true
					_ = visit(state)
					return nil
				default:
					if err := visit(x.leases.state); err != nil {
						return err
					}
					return errors.New("private cleanup path")
				}
			})
			_, err := x.g.Prepare(":1.20", strings.Repeat("a", 32), 1, grantInput())
			want := StorageFailure
			if mode == "swallow rejection" {
				want = Blocked
			}
			assertCode(t, err, want)
			if len(x.g.plans) != 0 || len(x.f.p.plans) != 0 {
				t.Fatal("broken backend issued a plan")
			}
		})
	}
}
func TestGuardFinalCleanupErrorDoesNotPermitReplay(t *testing.T) {
	x := newGuardFixture(t)
	p := x.prepare(t, grantInput())
	original := x.g.leases
	x.g.leases = leaseFunc(func(p Plan, exclusive bool, visit func(MutationState) error) error {
		err := original.WithLease(p, exclusive, visit)
		if err != nil {
			return err
		}
		if exclusive {
			return errors.New("private cleanup error")
		}
		return nil
	})
	_, err := x.g.Execute(":1.20", p.Identifier(), x.auth(t), x.effect(t))
	assertCode(t, err, StorageFailure)
	_, err = x.g.Execute(":1.20", p.Identifier(), x.auth(t), x.effect(t))
	assertCode(t, err, Denied)
	if x.effects != 1 || x.authorizations != 1 || x.leases.held {
		t.Fatal("cleanup failure replayed effect")
	}
}
func TestGuardRechecksRevocationDuringFinalGroupResolution(t *testing.T) {
	x := newGuardFixture(t)
	p := x.prepare(t, grantInput())
	base := x.g.resolveGroup
	x.g.resolveGroup = func(name string) (uint32, error) {
		gid, err := base(name)
		if x.leases.held && x.leases.exclusive {
			x.f.allow = false
		}
		return gid, err
	}
	_, err := x.g.Execute(":1.20", p.Identifier(), x.auth(t), x.effect(t))
	assertCode(t, err, Denied)
	if x.effects != 0 {
		t.Fatal("revocation during groups still dispatched")
	}
}
func TestGuardPrunesExpiredAdministrativePlans(t *testing.T) {
	x := newGuardFixture(t)
	x.prepare(t, grantInput())
	x.f.now = planTTL
	x.prepare(t, grantInput())
	if len(x.g.plans) != 1 || len(x.f.p.plans) != 1 {
		t.Fatal("expired administrative plans retained")
	}
}
func TestGuardRejectsUnownedOrUnconfiguredExecution(t *testing.T) {
	x := newGuardFixture(t)
	p := x.prepare(t, grantInput())
	before := x.leases.calls
	_, err := x.g.Execute(":1.21", p.Identifier(), x.auth(t), x.effect(t))
	assertCode(t, err, Denied)
	if x.leases.calls != before || x.authorizations != 0 {
		t.Fatal("other sender reached leases or auth")
	}
	_, err = x.g.Execute(":1.20", p.Identifier(), nil, x.effect(t))
	assertCode(t, err, Denied)
	_, err = x.g.Execute(":1.20", p.Identifier(), x.auth(t), nil)
	assertCode(t, err, UnsupportedOperation)
	x.g.leases = nil
	_, err = x.g.Execute(":1.20", p.Identifier(), x.auth(t), x.effect(t))
	assertCode(t, err, RecoveryRequired)
	if x.authorizations != 0 || x.effects != 0 {
		t.Fatal("unconfigured dispatch")
	}
}
