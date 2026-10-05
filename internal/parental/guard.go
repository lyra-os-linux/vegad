package parental

import "sync"

const (
	AuthorizationFailure Code = "AuthorizationFailure"
	MutationFailure      Code = "MutationFailure"
)

// MutationState is observed while the backend holds catalog, inventory and
// operation leases in that order. Proof must be decoded and identity-bound.
// Digests seal complete snapshots; booleans may not ignore corrupt or unknown
// negative entries. These are trusted observations, never client assertions.
type MutationState struct {
	Catalog                       Catalog
	InventoryDigest, LedgerDigest string
	Proof                         *Identity
	Barrier, Journal, Owner       bool
}

// MutationLeases must synchronously call visit once with all leases held,
// release on every return, and return the visitor's error unchanged. Final calls
// need an exclusive operation lease. FileLeases supplies the Linux backend.
type MutationLeases interface {
	WithLease(Plan, bool, func(MutationState) error) error
}
type AdministrativePlan struct {
	admission                     Plan
	method                        Method
	parameters                    Parameters
	inventoryDigest, ledgerDigest string
}

func (p AdministrativePlan) Identifier() string      { return p.admission.Identifier() }
func (p AdministrativePlan) Method() Method          { return p.method }
func (p AdministrativePlan) Parameters() Parameters  { return copyParameters(p.parameters) }
func (p AdministrativePlan) ParameterDigest() string { return p.parameters.Digest() }

type AuthorizationRequest struct {
	Method                  Method
	Association, Generation string
	Revision                uint64
	Target                  Identity
	Parameters              Parameters
	ParameterDigest         string
}

// Guard serializes mutations of an association across all guards sharing a
// Planner. Memory mutexes never cross storage, authorization or effect callbacks.
// It is not wired into UsersService; backend qualification is still required.
type Guard struct {
	planner      *Planner
	leases       MutationLeases
	resolveGroup func(string) (uint32, error)
	mu           sync.Mutex
	plans        map[string]AdministrativePlan
}

func NewGuard(planner *Planner, leases MutationLeases, resolveGroup func(string) (uint32, error)) *Guard {
	return &Guard{planner: planner, leases: leases, resolveGroup: resolveGroup, plans: map[string]AdministrativePlan{}}
}
func (g *Guard) checkState(plan Plan, state MutationState) error {
	if state.Catalog.digest != plan.catalogDigest {
		return Conflict
	}
	if state.Barrier || state.Journal || state.Owner {
		return Blocked
	}
	if state.Proof == nil {
		return RecoveryRequired
	}
	if *state.Proof != plan.account.Identity {
		return InvalidIdentity
	}
	if !hash.MatchString(state.InventoryDigest) || !hash.MatchString(state.LedgerDigest) {
		return StorageFailure
	}
	if _, err := Classify(state.Catalog, plan.account.Identity.UID, g.planner.observeIdentity, nil, nil); err != nil {
		return err
	}
	uid, err := g.planner.caller(plan.sender, plan.association)
	if err != nil || uid != plan.callerUID {
		return Denied
	}
	return g.planner.checkExpiry(plan)
}

// withState wraps a trusted backend and still refuses a missing/duplicate visit
// or one whose error was swallowed. Backends MUST NOT return success after a
// successful effect followed by failed lease cleanup.
func (g *Guard) withState(plan Plan, exclusive bool, visit func(MutationState) error) error {
	if g.leases == nil {
		return RecoveryRequired
	}
	calls := 0
	var visitorErr error
	err := g.leases.WithLease(plan, exclusive, func(state MutationState) error {
		calls++
		if calls != 1 {
			visitorErr = StorageFailure
			return visitorErr
		}
		visitorErr = visit(state)
		return visitorErr
	})
	if visitorErr != nil {
		return visitorErr
	}
	if err != nil {
		return safeLeaseError(err)
	}
	if calls != 1 {
		return StorageFailure
	}
	return nil
}
func safeLeaseError(err error) error {
	switch err {
	case Denied, Conflict, Busy, Blocked, RecoveryRequired, InvalidIdentity, InvalidCatalog, UnsafeCatalog, UnsupportedProducer, InvalidClock, StorageFailure, UnsafeState, InvalidInventory:
		return err
	}
	return StorageFailure
}
func (g *Guard) discard(id string) {
	g.planner.mu.Lock()
	defer g.planner.mu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.planner.plans, id)
	delete(g.plans, id)
}
func (g *Guard) Prepare(sender, association string, revision uint64, input Mutation) (AdministrativePlan, error) {
	if g == nil || g.planner == nil {
		return AdministrativePlan{}, Denied
	}
	if _, err := g.planner.caller(sender, association); err != nil {
		return AdministrativePlan{}, err
	}
	params, err := sealParameters(input, g.resolveGroup)
	if err != nil {
		return AdministrativePlan{}, err
	}
	admission, err := g.planner.Prepare(sender, association, revision)
	if err != nil {
		return AdministrativePlan{}, err
	}
	plan := AdministrativePlan{admission: admission, method: input.Method, parameters: copyParameters(params)}
	err = g.withState(admission, false, func(state MutationState) error {
		if err := g.checkState(admission, state); err != nil {
			return err
		}
		if err := revalidateGroups(params, g.resolveGroup); err != nil {
			return err
		}
		plan.inventoryDigest = state.InventoryDigest
		plan.ledgerDigest = state.LedgerDigest
		return nil
	})
	if err != nil {
		g.discard(admission.identifier)
		return AdministrativePlan{}, err
	}
	// Both maps are protected in planner->guard order; no callbacks run here.
	g.planner.mu.Lock()
	defer g.planner.mu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	now, err := g.planner.nowLocked()
	if err != nil {
		delete(g.planner.plans, admission.identifier)
		return AdministrativePlan{}, err
	}
	if now >= admission.expires {
		delete(g.planner.plans, admission.identifier)
		return AdministrativePlan{}, Conflict
	}
	if _, ok := g.planner.plans[admission.identifier]; !ok {
		return AdministrativePlan{}, Denied
	}
	for id := range g.plans {
		if _, exists := g.planner.plans[id]; !exists {
			delete(g.plans, id)
		}
	}
	g.plans[admission.identifier] = plan
	return plan, nil
}
func (g *Guard) consume(plan AdministrativePlan) error {
	g.planner.mu.Lock()
	defer g.planner.mu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	now, err := g.planner.nowLocked()
	if err != nil {
		return err
	}
	if now >= plan.admission.expires {
		return Conflict
	}
	if _, ok := g.planner.plans[plan.Identifier()]; !ok {
		return Denied
	}
	if _, ok := g.plans[plan.Identifier()]; !ok {
		return Denied
	}
	delete(g.planner.plans, plan.Identifier())
	delete(g.plans, plan.Identifier())
	return nil
}
func (g *Guard) Execute(sender, identifier string, authorize func(AuthorizationRequest) (bool, error), effect func(Method, Account, Parameters) error) (string, error) {
	if g == nil || g.planner == nil {
		return "", Denied
	}
	if authorize == nil {
		return "", Denied
	}
	if effect == nil {
		return "", UnsupportedOperation
	}
	admission, err := g.planner.Validate(sender, identifier)
	if err != nil {
		return "", err
	}
	g.mu.Lock()
	plan, ok := g.plans[identifier]
	g.mu.Unlock()
	if !ok {
		return "", Denied
	}
	if err = g.planner.beginMutation(admission); err != nil {
		return "", err
	}
	defer g.planner.endMutation(admission)
	check := func(state MutationState) error {
		if err := g.checkState(admission, state); err != nil {
			return err
		}
		if state.InventoryDigest != plan.inventoryDigest || state.LedgerDigest != plan.ledgerDigest {
			return Conflict
		}
		if err := revalidateGroups(plan.parameters, g.resolveGroup); err != nil {
			return err
		}
		// Group resolution is another trusted observation that may take time or
		// publish capability/identity changes; recheck before consuming the plan.
		return g.checkState(admission, state)
	}
	if err = g.withState(admission, false, check); err != nil {
		return "", err
	}
	request := AuthorizationRequest{Method: plan.method, Association: admission.association, Generation: admission.generation, Revision: admission.revision, Target: admission.account.Identity, Parameters: copyParameters(plan.parameters), ParameterDigest: plan.ParameterDigest()}
	allowed, err := authorize(request) // no storage or memory mutex held
	if err != nil {
		return "", AuthorizationFailure
	}
	if !allowed {
		return "", Denied
	}
	if _, err = g.planner.Validate(sender, identifier); err != nil {
		return "", err
	}
	err = g.withState(admission, true, func(state MutationState) error {
		if err := check(state); err != nil {
			return err
		}
		if err := g.consume(plan); err != nil {
			return err
		}
		if err := effect(plan.method, cloneAccount(admission.account), copyParameters(plan.parameters)); err != nil {
			return MutationFailure
		}
		observed, err := g.planner.observeIdentity(admission.account.Identity.UID)
		if err != nil || observed != admission.account.Identity {
			return InvalidIdentity
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return "OrdinaryMutationDispatched", nil
}
