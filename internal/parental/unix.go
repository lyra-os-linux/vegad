package parental

import (
	"context"
	"math"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// UnixAccount is a trusted local account observation. Supplementary groups must
// describe actual explicit memberships, not an inferred administrative flag.
type UnixAccount struct {
	Identity      Identity
	FullName      string
	Supplementary []Group
}
type UnixAdapter struct {
	observe      func(uint32) (UnixAccount, error)
	resolveGroup func(string) (uint32, error)
	run          func(context.Context, string, []string) error
}

func NewUnixAdapter(observe func(uint32) (UnixAccount, error), resolveGroup func(string) (uint32, error)) *UnixAdapter {
	return &UnixAdapter{observe: observe, resolveGroup: resolveGroup, run: func(ctx context.Context, path string, args []string) error {
		cmd := exec.CommandContext(ctx, path, args...)
		cmd.WaitDelay = time.Second
		// No shell, inherited stdin, environment overrides, or captured private output.
		return cmd.Run()
	}}
}
func checkedMemberships(account UnixAccount) (map[string]uint32, error) {
	groups := map[string]uint32{}
	for _, g := range account.Supplementary {
		if !accountName.MatchString(g.Name) || g.GID == 0 || g.GID == math.MaxUint32 {
			return nil, InvalidIdentity
		}
		if _, ok := groups[g.Name]; ok {
			return nil, InvalidIdentity
		}
		groups[g.Name] = g.GID
	}
	return groups, nil
}
func equalMemberships(a, b map[string]uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for name, gid := range a {
		if other, ok := b[name]; !ok || other != gid {
			return false
		}
	}
	return true
}

// Apply must only be supplied to Guard.Execute. It is not an authorization
// boundary or a substitute for the final catalog/inventory/operation leases.
func (a *UnixAdapter) Apply(method Method, account Account, params Parameters) error {
	if a == nil || a.observe == nil || a.resolveGroup == nil || a.run == nil {
		return RecoveryRequired
	}
	if account.Kind != "ordinary" {
		return UnsupportedOperation
	}
	id := account.Identity
	if !accountName.MatchString(id.User) || !accountName.MatchString(id.Group) || id.UID == 0 || id.GID == 0 || id.UID == math.MaxUint32 || id.GID == math.MaxUint32 {
		return InvalidIdentity
	}
	if method != UpdateUser && method != SetAdmin {
		return UnsupportedOperation
	}
	input := Mutation{Method: method, IsAdmin: params.IsAdmin()}
	if method == UpdateUser {
		input.FullName = params.FullName()
		for _, g := range params.Groups() {
			input.Groups = append(input.Groups, g.Name)
		}
	}
	rebuilt, err := sealParameters(input, a.resolveGroup)
	if err != nil || rebuilt.Digest() != params.Digest() {
		return Conflict
	}
	before, err := a.observe(account.Identity.UID)
	if err != nil || before.Identity != account.Identity {
		return InvalidIdentity
	}
	current, err := checkedMemberships(before)
	if err != nil {
		return err
	}
	wheel, err := a.resolveGroup("wheel")
	if err != nil || wheel == 0 || wheel == math.MaxUint32 {
		return Conflict
	}
	// A primary wheel membership cannot be removed with gpasswd -d or -G.
	if !params.IsAdmin() && (account.Identity.GID == wheel || account.Identity.Group == "wheel") {
		return UnsupportedOperation
	}
	expected := map[string]uint32{}
	for name, gid := range current {
		expected[name] = gid
	}
	var path string
	var args []string
	if method == SetAdmin {
		gid, present := current["wheel"]
		if present && gid != wheel {
			return Conflict
		}
		if params.IsAdmin() {
			if present || account.Identity.GID == wheel {
				return nil
			}
			expected["wheel"] = wheel
			path = "/usr/sbin/usermod"
			args = []string{"-aG", "wheel", "--", account.Identity.User}
		} else {
			if !present {
				return nil
			}
			delete(expected, "wheel")
			path = "/usr/bin/gpasswd"
			args = []string{"-d", account.Identity.User, "wheel"}
		}
	} else {
		expected = map[string]uint32{}
		names := []string{}
		for _, g := range params.Groups() {
			expected[g.Name] = g.GID
			names = append(names, g.Name)
		}
		sort.Strings(names)
		path = "/usr/sbin/usermod"
		args = []string{"-c", params.FullName(), "-G", strings.Join(names, ","), "--", account.Identity.User}
	}
	if err = revalidateGroups(params, a.resolveGroup); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = a.run(ctx, path, append([]string(nil), args...)); err != nil {
		return MutationFailure
	}
	after, err := a.observe(account.Identity.UID)
	if err != nil || after.Identity != account.Identity {
		return InvalidIdentity
	}
	observed, err := checkedMemberships(after)
	if err != nil {
		return err
	}
	if !equalMemberships(observed, expected) || method == UpdateUser && after.FullName != params.FullName() {
		return MutationFailure
	}
	if err = revalidateGroups(params, a.resolveGroup); err != nil {
		return err
	}
	return nil
}
