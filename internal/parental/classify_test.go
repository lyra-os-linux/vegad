package parental

import (
	"errors"
	"strings"
	"testing"
)

func observe(uid uint32) (Identity, error) {
	return Identity{User: "ordinaryuser", UID: uid, GID: 100, Group: "users"}, nil
}
func TestClassifyOrdinaryAndAbsent(t *testing.T) {
	c := decodeRow(t, fixtureRow())
	got, err := Classify(c, 1002, observe, nil, nil)
	if err != nil || got != OrdinaryEligible {
		t.Fatal(got, err)
	}
	_, err = Classify(c, 1003, observe, nil, nil)
	assertCode(t, err, RecoveryRequired)
	_, err = Classify(Catalog{}, 1002, observe, nil, nil)
	assertCode(t, err, RecoveryRequired)
	for _, uid := range []uint32{0, ^uint32(0)} {
		_, err = Classify(c, uid, observe, nil, nil)
		assertCode(t, err, InvalidIdentity)
	}
}
func TestClassifyBarrierPrecedesIdentityLookup(t *testing.T) {
	for _, state := range []string{"active", "provisioning", "transitioning", "recovering", "tombstone"} {
		t.Run(state, func(t *testing.T) {
			row := fixtureRow()
			a := first(row)
			a["state"] = state
			a["barrier"] = state == "active"
			a["operation"] = strings.Repeat("c", 32)
			c := decodeRow(t, row)
			called := false
			_, err := Classify(c, 1002, func(uid uint32) (Identity, error) { called = true; return observe(uid) }, nil, nil)
			assertCode(t, err, Blocked)
			if called {
				t.Fatal("identity lookup during blocked admission")
			}
		})
	}
}
func TestClassifyExactIdentity(t *testing.T) {
	c := decodeRow(t, fixtureRow())
	callbacks := map[string]func(uint32) (Identity, error){
		"nil": nil, "failed": func(uint32) (Identity, error) { return Identity{}, errors.New("private NSS details") },
		"name":  func(uid uint32) (Identity, error) { i, _ := observe(uid); i.User = "peer"; return i, nil },
		"uid":   func(uid uint32) (Identity, error) { i, _ := observe(uid); i.UID++; return i, nil },
		"gid":   func(uid uint32) (Identity, error) { i, _ := observe(uid); i.GID++; return i, nil },
		"group": func(uid uint32) (Identity, error) { i, _ := observe(uid); i.Group = "peer"; return i, nil },
	}
	for name, callback := range callbacks {
		t.Run(name, func(t *testing.T) {
			_, err := Classify(c, 1002, callback, nil, nil)
			assertCode(t, err, InvalidIdentity)
		})
	}
}
func applied(a Account) (Applied, error) {
	return Applied{a.Generation, a.Policy, append([]Adapter(nil), a.Adapters...)}, nil
}
func TestClassifySupervisedRequiresExactObservation(t *testing.T) {
	c := decodeRow(t, supervisedRow())
	required := []string{"pam", "flatpak"}
	got, err := Classify(c, 1002, observe, required, applied)
	if err != nil || got != SupervisedEligible {
		t.Fatal(got, err)
	}
	for _, names := range [][]string{nil, {"pam"}, {"pam", "pam"}, {"pam", "unknown"}, {"pam", "flatpak", "unknown"}} {
		called := false
		_, err := Classify(c, 1002, observe, names, func(a Account) (Applied, error) { called = true; return applied(a) })
		assertCode(t, err, UnsupportedProducer)
		if called {
			t.Fatal("unknown coverage reached observer")
		}
	}
	_, err = Classify(c, 1002, observe, required, nil)
	assertCode(t, err, UnsupportedProducer)
	for name, callback := range map[string]func(Account) (Applied, error){
		"error": func(Account) (Applied, error) { return Applied{}, errors.New("secret adapter path") },
		"generation": func(a Account) (Applied, error) {
			r, _ := applied(a)
			r.Generation = strings.Repeat("f", 32)
			return r, nil
		},
		"policy": func(a Account) (Applied, error) {
			r, _ := applied(a)
			r.Policy = strings.Repeat("f", 64)
			return r, nil
		},
		"digest": func(a Account) (Applied, error) {
			r, _ := applied(a)
			r.Adapters[0].Digest = strings.Repeat("f", 64)
			return r, nil
		},
		"missing":       func(a Account) (Applied, error) { r, _ := applied(a); r.Adapters = r.Adapters[:1]; return r, nil },
		"duplicate":     func(a Account) (Applied, error) { r, _ := applied(a); r.Adapters[1] = r.Adapters[0]; return r, nil },
		"mutated input": func(a Account) (Applied, error) { a.Adapters[0].Digest = strings.Repeat("f", 64); return applied(a) },
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Classify(c, 1002, observe, required, callback)
			assertCode(t, err, UnsupportedProducer)
		})
	}
	if c.Accounts()[0].Adapters[0].Digest != strings.Repeat("e", 64) {
		t.Fatal("observer changed stored catalog")
	}
}
