package parental

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestBusAuthoritySubjectActionsAndDetails(t *testing.T) {
	a := NewBusAuthority(nil, "org.lyraos.fixture.profile", "org.lyraos.fixture.admin")
	for _, method := range []Method{UpdateUser, SetAdmin} {
		calls := 0
		a.call = func(ctx context.Context, name string, path dbus.ObjectPath, member string, args ...interface{}) *dbus.Call {
			calls++
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("unbounded call")
			}
			if calls == 1 {
				if name != "org.freedesktop.DBus" || path != "/org/freedesktop/DBus" || member != "org.freedesktop.DBus.GetConnectionUnixUser" || args[0] != ":1.20" {
					t.Fatal(name, path, member, args)
				}
				return &dbus.Call{Body: []interface{}{uint32(1000)}}
			}
			if name != "org.freedesktop.PolicyKit1" || path != "/org/freedesktop/PolicyKit1/Authority" || member != "org.freedesktop.PolicyKit1.Authority.CheckAuthorization" {
				t.Fatal(name, path, member)
			}
			subject := args[0].(busSubject)
			if subject.Kind != "system-bus-name" || len(subject.Details) != 1 || subject.Details["name"].Value() != ":1.20" || args[1] != a.actions[method] || args[3] != uint32(1) || !token.MatchString(args[4].(string)) {
				t.Fatalf("arguments: %#v", args)
			}
			details := args[2].(map[string]string)
			if len(details) != 6 || details["method"] != string(method) || details["association"] != "association" || details["generation"] != "generation" || details["revision"] != "7" || details["target-uid"] != "1002" || details["parameter-digest"] != strings.Repeat("f", 64) {
				t.Fatal(details)
			}
			return &dbus.Call{Body: []interface{}{[]interface{}{true, false, map[string]string{}}}}
		}
		ok, err := a.Authorize(":1.20", AuthorizationRequest{Method: method, Association: "association", Generation: "generation", Revision: 7, Target: Identity{UID: 1002}, ParameterDigest: strings.Repeat("f", 64)})
		if !ok || err != nil || calls != 2 {
			t.Fatal(ok, err, calls)
		}
	}
}
func TestBusAuthorityFailsClosed(t *testing.T) {
	for _, name := range []string{"missing", "well-known", "unknown-method", "bad-action", "uid-error", "uid-type", "uid-reserved", "denied", "challenge", "contradictory", "polkit-error", "polkit-type"} {
		t.Run(name, func(t *testing.T) {
			a := NewBusAuthority(nil, "org.lyraos.fixture.profile", "org.lyraos.fixture.admin")
			sender := ":1.20"
			request := AuthorizationRequest{Method: SetAdmin}
			calls := 0
			a.call = func(_ context.Context, _ string, _ dbus.ObjectPath, method string, _ ...interface{}) *dbus.Call {
				calls++
				if strings.HasSuffix(method, "GetConnectionUnixUser") {
					switch name {
					case "uid-error":
						return &dbus.Call{Err: errors.New("private UID failure")}
					case "uid-type":
						return &dbus.Call{Body: []interface{}{"1000"}}
					case "uid-reserved":
						return &dbus.Call{Body: []interface{}{uint32(math.MaxUint32)}}
					}
					return &dbus.Call{Body: []interface{}{uint32(0)}} // no implicit grant
				}
				switch name {
				case "denied":
					return &dbus.Call{Body: []interface{}{[]interface{}{false, false, map[string]string{}}}}
				case "challenge":
					return &dbus.Call{Body: []interface{}{[]interface{}{false, true, map[string]string{}}}}
				case "contradictory":
					return &dbus.Call{Body: []interface{}{[]interface{}{true, true, map[string]string{}}}}
				case "polkit-type":
					return &dbus.Call{Body: []interface{}{"yes"}}
				}
				return &dbus.Call{Err: errors.New("private polkit failure")}
			}
			switch name {
			case "missing":
				a.call = nil
			case "well-known":
				sender = "org.lyraos.Client"
			case "unknown-method":
				request.Method = "RemoveUser"
			case "bad-action":
				a.actions[SetAdmin] = "--action"
			}
			ok, err := a.Authorize(sender, request)
			if ok {
				t.Fatal("fail-open")
			}
			if err != nil && err != Denied && err != AuthorizationFailure && err != UnsupportedOperation {
				t.Fatal("unsafe error", err)
			}
			if (name == "missing" || name == "well-known" || name == "unknown-method" || name == "bad-action") && calls != 0 {
				t.Fatal("unnecessary call")
			}
		})
	}
}
func TestBusAuthorityTimeoutCancelsExactCheck(t *testing.T) {
	a := NewBusAuthority(nil, "org.lyraos.fixture.profile", "org.lyraos.fixture.admin")
	a.authTimeout = time.Millisecond
	id := ""
	cancelled := false
	a.call = func(ctx context.Context, _ string, _ dbus.ObjectPath, member string, args ...interface{}) *dbus.Call {
		switch {
		case strings.HasSuffix(member, "GetConnectionUnixUser"):
			return &dbus.Call{Body: []interface{}{uint32(1000)}}
		case strings.HasSuffix(member, "CheckAuthorization") && !strings.HasSuffix(member, "CancelCheckAuthorization"):
			id = args[4].(string)
			<-ctx.Done()
			return &dbus.Call{Err: ctx.Err()}
		default:
			if member != "org.freedesktop.PolicyKit1.Authority.CancelCheckAuthorization" || len(args) != 1 || args[0] != id || ctx.Err() != nil {
				t.Fatal(member, args)
			}
			cancelled = true
			return &dbus.Call{}
		}
	}
	ok, err := a.Authorize(":1.20", AuthorizationRequest{Method: SetAdmin})
	if ok || err != AuthorizationFailure || !cancelled {
		t.Fatal(ok, err, cancelled)
	}
}
func TestAdministrativeServiceGuardComposition(t *testing.T) {
	for _, mode := range []string{"allow", "deny", "replay", "revoke", "barrier"} {
		t.Run(mode, func(t *testing.T) {
			x := newGuardFixture(t)
			a := NewBusAuthority(nil, "org.lyraos.fixture.profile", "org.lyraos.fixture.admin")
			auths := 0
			a.call = func(_ context.Context, _ string, _ dbus.ObjectPath, member string, _ ...interface{}) *dbus.Call {
				if strings.HasSuffix(member, "GetConnectionUnixUser") {
					return &dbus.Call{Body: []interface{}{uint32(1000)}}
				}
				assert := x.auth(t)
				assert(AuthorizationRequest{})
				auths++
				if mode == "barrier" {
					x.leases.state.Barrier = true
				}
				if mode == "revoke" {
					x.f.allow = false
				}
				return &dbus.Call{Body: []interface{}{[]interface{}{mode != "deny", false, map[string]string{}}}}
			}
			s := NewAdministrativeService(x.g, a, x.effect(t))
			id, err := s.PrepareSetAdmin(":1.20", strings.Repeat("a", 32), 1, true)
			if err != nil {
				t.Fatal(err)
			}
			sender := dbus.Sender(":1.20")
			if mode == "replay" {
				sender = ":1.21"
			}
			result, e := s.Execute(sender, id)
			if mode == "allow" {
				if e != nil || result != "OrdinaryMutationDispatched" || x.effects != 1 {
					t.Fatal(result, e, x.effects)
				}
				_, e = s.Execute(sender, id)
				if e == nil || e.Name != administrativeErrorPrefix+string(Denied) || auths != 1 {
					t.Fatal(e, auths)
				}
			} else {
				code := Denied
				if mode == "barrier" {
					code = Blocked
				}
				if e == nil || e.Name != administrativeErrorPrefix+string(code) || x.effects != 0 {
					t.Fatal(e, x.effects)
				}
				if mode == "replay" && auths != 0 {
					t.Fatal("replay prompted")
				}
			}
		})
	}
	var s *AdministrativeService
	_, e := s.Execute(":1.20", "id")
	if e == nil {
		t.Fatal("nil service allowed")
	}
}
