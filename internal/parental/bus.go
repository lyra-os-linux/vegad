package parental

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"time"

	"github.com/godbus/dbus/v5"
)

type busSubject struct {
	Kind    string
	Details map[string]dbus.Variant
}
type busAuthorization struct {
	Authorized bool
	Challenge  bool
	Details    map[string]string
}

// BusAuthority uses a service-owned system-bus connection. Action IDs are
// trusted server configuration, never request arguments. It grants no capability
// or guardian role: Planner must independently check those on every observation.
type BusAuthority struct {
	conn                    *dbus.Conn
	actions                 map[Method]string
	call                    func(context.Context, string, dbus.ObjectPath, string, ...interface{}) *dbus.Call
	uidTimeout, authTimeout time.Duration
}

var actionName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*(\.[A-Za-z][A-Za-z0-9_-]*)+$`)

func NewBusAuthority(conn *dbus.Conn, updateAction, adminAction string) *BusAuthority {
	a := &BusAuthority{conn: conn, actions: map[Method]string{UpdateUser: updateAction, SetAdmin: adminAction}, uidTimeout: 5 * time.Second, authTimeout: 45 * time.Second}
	if conn != nil {
		a.call = func(ctx context.Context, name string, path dbus.ObjectPath, method string, args ...interface{}) *dbus.Call {
			return conn.Object(name, path).CallWithContext(ctx, method, 0, args...)
		}
	}
	return a
}

func (a *BusAuthority) ResolveSender(sender string) (uint32, error) {
	if a == nil || a.call == nil || !uniqueSender.MatchString(sender) {
		return 0, Denied
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.uidTimeout)
	defer cancel()
	var uid uint32
	if err := a.call(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus.GetConnectionUnixUser", sender).Store(&uid); err != nil || uid == math.MaxUint32 {
		return 0, Denied
	}
	return uid, nil
}

// Authorize binds the subject to the injected method sender, not the target or
// daemon UID. The guard calls this outside all leases and revalidates afterwards.
func (a *BusAuthority) Authorize(sender string, request AuthorizationRequest) (bool, error) {
	if a == nil || a.call == nil || !uniqueSender.MatchString(sender) {
		return false, Denied
	}
	action := a.actions[request.Method]
	if !actionName.MatchString(action) {
		return false, UnsupportedOperation
	}
	if _, err := a.ResolveSender(sender); err != nil {
		return false, Denied
	}
	id, err := randomIdentifier()
	if err != nil {
		return false, AuthorizationFailure
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.authTimeout)
	defer cancel()
	subject := busSubject{"system-bus-name", map[string]dbus.Variant{"name": dbus.MakeVariant(sender)}}
	details := map[string]string{"method": string(request.Method), "association": request.Association, "generation": request.Generation, "revision": strconv.FormatUint(request.Revision, 10), "target-uid": strconv.FormatUint(uint64(request.Target.UID), 10), "parameter-digest": request.ParameterDigest}
	var result busAuthorization
	err = a.call(ctx, "org.freedesktop.PolicyKit1", "/org/freedesktop/PolicyKit1/Authority", "org.freedesktop.PolicyKit1.Authority.CheckAuthorization", subject, action, details, uint32(1), id).Store(&result)
	if ctx.Err() != nil {
		// Dropping a local wait alone does not cancel an interactive polkit check.
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		a.call(cleanup, "org.freedesktop.PolicyKit1", "/org/freedesktop/PolicyKit1/Authority", "org.freedesktop.PolicyKit1.Authority.CancelCheckAuthorization", id)
		return false, AuthorizationFailure
	}
	if err != nil {
		return false, AuthorizationFailure
	}
	return result.Authorized && !result.Challenge, nil
}

// AdministrativeService is an unexported-by-default composition. Deployment
// must supply a qualified Planner/capability source and system bus; constructing
// it neither exports an object nor changes UsersService. No client UID/action/
// execution payload is accepted. godbus injects dbus.Sender from the wire header.
type AdministrativeService struct {
	guard     *Guard
	authority *BusAuthority
	effect    func(Method, Account, Parameters) error
}

func NewAdministrativeService(guard *Guard, authority *BusAuthority, effect func(Method, Account, Parameters) error) *AdministrativeService {
	return &AdministrativeService{guard: guard, authority: authority, effect: effect}
}

const administrativeErrorPrefix = "org.lyraos.Parental.Error."

func administrativeError(err error) *dbus.Error {
	code, ok := err.(Code)
	if !ok {
		code = StorageFailure
	}
	return dbus.NewError(administrativeErrorPrefix+string(code), []interface{}{string(code)})
}
func (s *AdministrativeService) prepare(sender dbus.Sender, association string, revision uint64, input Mutation) (string, *dbus.Error) {
	if s == nil || s.guard == nil || s.authority == nil || s.effect == nil {
		return "", administrativeError(Denied)
	}
	plan, err := s.guard.Prepare(string(sender), association, revision, input)
	if err != nil {
		return "", administrativeError(err)
	}
	return plan.Identifier(), nil
}
func (s *AdministrativeService) PrepareUpdateUser(sender dbus.Sender, association string, revision uint64, fullName string, groups []string, admin bool) (string, *dbus.Error) {
	return s.prepare(sender, association, revision, Mutation{Method: UpdateUser, FullName: fullName, Groups: groups, IsAdmin: admin})
}
func (s *AdministrativeService) PrepareSetAdmin(sender dbus.Sender, association string, revision uint64, admin bool) (string, *dbus.Error) {
	return s.prepare(sender, association, revision, Mutation{Method: SetAdmin, IsAdmin: admin})
}
func (s *AdministrativeService) Execute(sender dbus.Sender, identifier string) (string, *dbus.Error) {
	if s == nil || s.guard == nil || s.authority == nil || s.effect == nil {
		return "", administrativeError(Denied)
	}
	result, err := s.guard.Execute(string(sender), identifier, func(request AuthorizationRequest) (bool, error) {
		return s.authority.Authorize(string(sender), request)
	}, s.effect)
	if err != nil {
		return "", administrativeError(err)
	}
	return result, nil
}
