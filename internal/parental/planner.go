package parental

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"regexp"
	"sync"
	"time"
)

const (
	Denied               Code = "Denied"
	Conflict             Code = "Conflict"
	InvalidClock         Code = "InvalidClock"
	StorageFailure       Code = "StorageFailure"
	UnsupportedOperation Code = "UnsupportedOperation"
)
const planTTL = 60 * time.Second
const maxPlans = 128

var uniqueSender = regexp.MustCompile(`^:[0-9]+\.[0-9]+$`)

// Snapshotter must supply a validated immutable catalog, not client data.
type Snapshotter interface{ Snapshot() (Catalog, error) }

// Plan is private server state. Accessors cannot change stored bindings, and an
// identifier is not authorization to mutate an account or retire admission.
type Plan struct {
	identifier, sender, association, generation, catalogDigest string
	callerUID                                                  uint32
	revision                                                   uint64
	account                                                    Account
	expires                                                    time.Duration
}

func (p Plan) Identifier() string    { return p.identifier }
func (p Plan) Association() string   { return p.association }
func (p Plan) Generation() string    { return p.generation }
func (p Plan) Revision() uint64      { return p.revision }
func (p Plan) CatalogDigest() string { return p.catalogDigest }
func (p Plan) Account() Account      { return cloneAccount(p.account) }
func (p Plan) Effect() string        { return "deny-new-admission-only" }

// Planner prepares ordinary-admission plans. Callbacks are trusted service
// adapters and must be safe for concurrent calls. Capability is checked before
// any target lookup; root has no implicit capability. This does not ask Polkit,
// execute commands, write state or qualify supervised lifecycle operations.
type Planner struct {
	source          Snapshotter
	resolveSender   func(string) (uint32, error)
	capable         func(uint32, string) (bool, error)
	observeIdentity func(uint32) (Identity, error)
	mu              sync.Mutex
	plans           map[string]Plan
	running         map[string]bool
	clock           func() time.Duration
	last            time.Duration
	identifier      func() (string, error)
}

func NewPlanner(source Snapshotter, resolveSender func(string) (uint32, error), capable func(uint32, string) (bool, error), observeIdentity func(uint32) (Identity, error)) *Planner {
	start := time.Now()
	return &Planner{source: source, resolveSender: resolveSender, capable: capable, observeIdentity: observeIdentity,
		plans: map[string]Plan{}, clock: func() time.Duration { return time.Since(start) }, identifier: randomIdentifier}
}
func randomIdentifier() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", StorageFailure
	}
	return hex.EncodeToString(bytes[:]), nil
}
func (p *Planner) caller(sender, association string) (uint32, error) {
	if p == nil || !uniqueSender.MatchString(sender) || !token.MatchString(association) || p.resolveSender == nil || p.capable == nil {
		return 0, Denied
	}
	uid, err := p.resolveSender(sender)
	if err != nil || uid == math.MaxUint32 {
		return 0, Denied
	}
	allowed, err := p.capable(uid, association)
	if err != nil || !allowed {
		return 0, Denied
	}
	return uid, nil
}
func (p *Planner) snapshot() (Catalog, error) {
	if p.source == nil {
		return Catalog{}, RecoveryRequired
	}
	c, err := p.source.Snapshot()
	if err != nil {
		var code Code
		if errors.As(err, &code) {
			switch code {
			case InvalidCatalog, UnsafeCatalog, RecoveryRequired, Busy:
				return Catalog{}, code
			}
		}
		return Catalog{}, StorageFailure
	}
	if c.revision == 0 || !hash.MatchString(c.digest) {
		return Catalog{}, InvalidCatalog
	}
	return c, nil
}

// nowLocked is called only under mu. Production clock is monotonic elapsed
// time; test injection stays private. Clock failure never extends a deadline.
func (p *Planner) nowLocked() (time.Duration, error) {
	if p.clock == nil {
		return 0, InvalidClock
	}
	now := p.clock()
	if now < 0 || now < p.last || now > time.Duration(math.MaxInt64)-planTTL {
		return 0, InvalidClock
	}
	p.last = now
	return now, nil
}
func (p *Planner) checkExpiry(plan Plan) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	now, err := p.nowLocked()
	if err != nil {
		return err
	}
	if now >= plan.expires {
		return Conflict
	}
	if _, ok := p.plans[plan.identifier]; !ok {
		return Denied
	}
	return nil
}
func (p *Planner) Prepare(sender, association string, expectedRevision uint64) (Plan, error) {
	uid, err := p.caller(sender, association)
	if err != nil {
		return Plan{}, err
	}
	if expectedRevision == 0 || expectedRevision > math.MaxInt64 {
		return Plan{}, Conflict
	}
	c, err := p.snapshot()
	if err != nil {
		return Plan{}, err
	}
	var account *Account
	for _, a := range c.accounts {
		if a.Association == association {
			copy := cloneAccount(a)
			account = &copy
			break
		}
	}
	if account == nil {
		return Plan{}, RecoveryRequired
	}
	if account.Revision != expectedRevision {
		return Plan{}, Conflict
	}
	if account.Kind != "ordinary" {
		return Plan{}, UnsupportedOperation
	}
	if _, err = Classify(c, account.Identity.UID, p.observeIdentity, nil, nil); err != nil {
		return Plan{}, err
	}
	// Recheck capability after potentially slow storage/NSS observations.
	finalUID, err := p.caller(sender, association)
	if err != nil || finalUID != uid {
		return Plan{}, Denied
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now, err := p.nowLocked()
	if err != nil {
		return Plan{}, err
	}
	for id, plan := range p.plans {
		if now >= plan.expires {
			delete(p.plans, id)
		}
	}
	if len(p.plans) >= maxPlans {
		return Plan{}, Busy
	}
	// Never overwrite a live plan even if the entropy source fails or repeats.
	for attempt := 0; attempt < 8; attempt++ {
		if p.identifier == nil {
			return Plan{}, StorageFailure
		}
		id, err := p.identifier()
		if err != nil || !token.MatchString(id) {
			return Plan{}, StorageFailure
		}
		if _, exists := p.plans[id]; exists {
			continue
		}
		plan := Plan{identifier: id, sender: sender, association: association, generation: account.Generation,
			catalogDigest: c.digest, callerUID: uid, revision: account.Revision, account: cloneAccount(*account), expires: now + planTTL}
		p.plans[id] = plan
		return plan, nil
	}
	return Plan{}, Busy
}

// Validate accepts only the server's identifier, rechecks live sender,
// capability, catalog and identity, and checks expiry again after observations.
// It grants neither a filesystem lease nor interactive authorization. The final
// execution guard must revalidate under its leases and consume before dispatch.
func (p *Planner) Validate(sender, identifier string) (Plan, error) {
	if p == nil || !token.MatchString(identifier) {
		return Plan{}, Denied
	}
	p.mu.Lock()
	plan, ok := p.plans[identifier]
	p.mu.Unlock()
	if !ok || plan.sender != sender {
		return Plan{}, Denied
	}
	uid, err := p.caller(sender, plan.association)
	if err != nil || uid != plan.callerUID {
		return Plan{}, Denied
	}
	if err = p.checkExpiry(plan); err != nil {
		return Plan{}, err
	}
	c, err := p.snapshot()
	if err != nil {
		return Plan{}, err
	}
	if c.digest != plan.catalogDigest {
		return Plan{}, Conflict
	}
	if _, err = Classify(c, plan.account.Identity.UID, p.observeIdentity, nil, nil); err != nil {
		return Plan{}, err
	}
	uid, err = p.caller(sender, plan.association)
	if err != nil || uid != plan.callerUID {
		return Plan{}, Denied
	}
	if err = p.checkExpiry(plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// Association reservation spans interaction and dispatch, shared by all guards.
func (p *Planner) beginMutation(plan Plan) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running == nil {
		p.running = map[string]bool{}
	}
	if p.running[plan.association] {
		return Busy
	}
	p.running[plan.association] = true
	return nil
}
func (p *Planner) endMutation(plan Plan) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.running, plan.association)
}
