package parental

import "sort"

func sortAdapters(a []Adapter) { sort.Slice(a, func(i, j int) bool { return a[i].Name < a[j].Name }) }

// Applied is an observation from trusted adapters, never from the GUI.
type Applied struct {
	Generation, Policy string
	Adapters           []Adapter
}
type Eligibility string

const (
	OrdinaryEligible   Eligibility = "OrdinaryEligible"
	SupervisedEligible Eligibility = "SupervisedEligible"
)

// Classify checks eligibility, not authorization or producer enforcement.
// A missing/invalid account never falls back to ordinary. Callers must overlay
// operation barriers and apply each channel's own controls before admission.
func Classify(c Catalog, uid uint32, observeIdentity func(uint32) (Identity, error), required []string, observeAdapters func(Account) (Applied, error)) (Eligibility, error) {
	if uid == 0 || uid == ^uint32(0) {
		return "", InvalidIdentity
	}
	var account *Account
	for _, a := range c.accounts {
		if a.Identity.UID == uid {
			copy := cloneAccount(a)
			account = &copy
			break
		}
	}
	if account == nil {
		return "", RecoveryRequired
	}
	a := *account
	if a.Barrier || a.State != "active" {
		return "", Blocked
	}
	if observeIdentity == nil {
		return "", InvalidIdentity
	}
	observed, err := observeIdentity(uid)
	if err != nil || observed != a.Identity {
		return "", InvalidIdentity
	}
	if a.Kind == "ordinary" {
		return OrdinaryEligible, nil
	}
	if len(required) == 0 || len(required) != len(a.Adapters) || observeAdapters == nil {
		return "", UnsupportedProducer
	}
	names := map[string]bool{}
	for _, name := range required {
		if !accountName.MatchString(name) || names[name] {
			return "", UnsupportedProducer
		}
		names[name] = true
	}
	for _, adapter := range a.Adapters {
		if !names[adapter.Name] {
			return "", UnsupportedProducer
		}
	}
	expected := cloneAccount(a) // callback receives a separate slice
	applied, err := observeAdapters(cloneAccount(a))
	if err != nil || applied.Generation != expected.Generation || applied.Policy != expected.Policy || len(applied.Adapters) != len(expected.Adapters) {
		return "", UnsupportedProducer
	}
	actual := append([]Adapter(nil), applied.Adapters...)
	sortAdapters(actual)
	for n, adapter := range expected.Adapters {
		if actual[n] != adapter {
			return "", UnsupportedProducer
		}
	}
	return SupervisedEligible, nil
}
