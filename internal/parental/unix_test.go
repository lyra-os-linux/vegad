package parental

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestUnixAdapterFixedArgvAndPostconditions(t *testing.T) {
	for _, method := range []Method{UpdateUser, SetAdmin} {
		for _, admin := range []bool{false, true} {
			id, _ := observe(1002)
			state := UnixAccount{Identity: id, FullName: "original", Supplementary: []Group{{"users", 100}, {"wheel", 10}}}
			if admin {
				state.Supplementary = state.Supplementary[:1]
			}
			input := Mutation{Method: method, IsAdmin: admin}
			if method == UpdateUser {
				input.FullName = "Lyra $(literal)"
				input.Groups = []string{"users"}
			}
			params, err := sealParameters(input, groupResolver)
			if err != nil {
				t.Fatal(err)
			}
			a := NewUnixAdapter(func(uint32) (UnixAccount, error) { return state, nil }, groupResolver)
			calls := 0
			a.run = func(ctx context.Context, path string, args []string) error {
				calls++
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("no command deadline")
				}
				wantPath := "/usr/sbin/usermod"
				want := []string{"-aG", "wheel", "--", "ordinaryuser"}
				if !admin {
					wantPath = "/usr/bin/gpasswd"
					want = []string{"-d", "ordinaryuser", "wheel"}
				}
				if method == UpdateUser {
					wantPath = "/usr/sbin/usermod"
					groups := "users"
					if admin {
						groups = "users,wheel"
					}
					want = []string{"-c", "Lyra $(literal)", "-G", groups, "--", "ordinaryuser"}
					state.FullName = input.FullName
				}
				if path != wantPath || !reflect.DeepEqual(args, want) {
					t.Fatal("unexpected argv", path, args)
				}
				state.Supplementary = []Group{{"users", 100}}
				if admin {
					state.Supplementary = append(state.Supplementary, Group{"wheel", 10})
				}
				return nil
			}
			if err = a.Apply(method, Account{Kind: "ordinary", Identity: id}, params); err != nil || calls != 1 {
				t.Fatal(err, calls)
			}
		}
	}
}
func TestUnixAdapterNoopAndPrimaryWheel(t *testing.T) {
	for _, admin := range []bool{false, true} {
		id, _ := observe(1002)
		state := UnixAccount{Identity: id, Supplementary: []Group{{"users", 100}}}
		if admin {
			state.Supplementary = append(state.Supplementary, Group{"wheel", 10})
		}
		a := NewUnixAdapter(func(uint32) (UnixAccount, error) { return state, nil }, groupResolver)
		a.run = func(context.Context, string, []string) error {
			t.Fatal("already applied status executed command")
			return nil
		}
		p, _ := sealParameters(Mutation{Method: SetAdmin, IsAdmin: admin}, groupResolver)
		if err := a.Apply(SetAdmin, Account{Kind: "ordinary", Identity: id}, p); err != nil {
			t.Fatal(err)
		}
	}
	id, _ := observe(1002)
	id.GID = 10
	id.Group = "wheel"
	a := NewUnixAdapter(func(uint32) (UnixAccount, error) { return UnixAccount{Identity: id}, nil }, groupResolver)
	a.run = func(context.Context, string, []string) error { t.Fatal("primary wheel removal executed"); return nil }
	p, _ := sealParameters(Mutation{Method: SetAdmin}, groupResolver)
	assertCode(t, a.Apply(SetAdmin, Account{Kind: "ordinary", Identity: id}, p), UnsupportedOperation)
}
func TestUnixAdapterRejectsStaleIdentityGroupsAndFailedEffects(t *testing.T) {
	for _, mode := range []string{"identity", "group", "command", "profile", "memberships", "after identity"} {
		t.Run(mode, func(t *testing.T) {
			id, _ := observe(1002)
			state := UnixAccount{Identity: id, FullName: "old", Supplementary: []Group{{"users", 100}}}
			p, _ := sealParameters(Mutation{Method: UpdateUser, FullName: "new", Groups: []string{"users"}}, groupResolver)
			resolver := groupResolver
			if mode == "identity" {
				state.Identity.UID++
			}
			if mode == "group" {
				resolver = func(string) (uint32, error) { return 101, nil }
			}
			a := NewUnixAdapter(func(uint32) (UnixAccount, error) { return state, nil }, resolver)
			calls := 0
			a.run = func(context.Context, string, []string) error {
				calls++
				state.FullName = "new"
				switch mode {
				case "command":
					return errors.New("private command output")
				case "profile":
					state.FullName = "wrong"
				case "memberships":
					state.Supplementary = nil
				case "after identity":
					state.Identity.GID++
				}
				return nil
			}
			err := a.Apply(UpdateUser, Account{Kind: "ordinary", Identity: id}, p)
			want := MutationFailure
			if mode == "identity" || mode == "after identity" {
				want = InvalidIdentity
			}
			if mode == "group" {
				want = Conflict
			}
			assertCode(t, err, want)
			if (mode == "identity" || mode == "group") && calls != 0 {
				t.Fatal("stale state dispatched")
			}
		})
	}
}
