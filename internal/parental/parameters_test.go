package parental

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func groupResolver(name string) (uint32, error) {
	if name == "users" {
		return 100, nil
	}
	if name == "wheel" {
		return 10, nil
	}
	return 0, errors.New("unknown group")
}
func TestParametersStrictInputs(t *testing.T) {
	invalid := []Mutation{
		{Method: "RemoveUser"}, {Method: SetAdmin, FullName: "not allowed"}, {Method: SetAdmin, Groups: []string{"users"}},
		{Method: UpdateUser, FullName: ""}, {Method: UpdateUser, FullName: "  "}, {Method: UpdateUser, FullName: strings.Repeat("x", 129)},
		{Method: UpdateUser, FullName: "a:b"}, {Method: UpdateUser, FullName: "line\n"}, {Method: UpdateUser, FullName: "\x7f"}, {Method: UpdateUser, FullName: string([]byte{255})},
		{Method: UpdateUser, FullName: "valid", Groups: []string{"--help"}},
		{Method: UpdateUser, FullName: "valid", Groups: []string{"users", "users"}},
		{Method: UpdateUser, FullName: "valid", Groups: []string{"unknown"}},
		{Method: UpdateUser, FullName: "valid", Groups: []string{"wheel"}},
	}
	for n, input := range invalid {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			_, err := sealParameters(input, groupResolver)
			want := InvalidParameters
			if input.Method == "RemoveUser" {
				want = UnsupportedOperation
			}
			assertCode(t, err, want)
		})
	}
	for _, gid := range []uint32{0, ^uint32(0)} {
		_, err := sealParameters(grantInput(), func(string) (uint32, error) { return gid, nil })
		assertCode(t, err, InvalidParameters)
	}
	_, err := sealParameters(grantInput(), nil)
	assertCode(t, err, RecoveryRequired)
}
func TestParametersNormalizeAndPreserveDeltaMeaning(t *testing.T) {
	p, err := sealParameters(Mutation{Method: UpdateUser, FullName: "José $(literal)", Groups: []string{"users"}, IsAdmin: true}, groupResolver)
	if err != nil || p.FullName() != "José $(literal)" || !p.IsAdmin() || len(p.Groups()) != 2 || p.Groups()[0].Name != "users" || p.Groups()[1].Name != "wheel" {
		t.Fatal(p, err)
	}
	list := p.Groups()
	list[0].GID++
	if p.Groups()[0].GID != 100 {
		t.Fatal("mutable groups accessor")
	}
	remove, err := sealParameters(Mutation{Method: SetAdmin, IsAdmin: false}, groupResolver)
	if err != nil || len(remove.Groups()) != 1 || remove.Groups()[0].Name != "wheel" || remove.IsAdmin() || remove.FullName() != "" {
		t.Fatal("SetAdmin delta became full group replacement", err)
	}
	empty, err := sealParameters(Mutation{Method: UpdateUser, FullName: "valid"}, groupResolver)
	if err != nil || len(empty.Groups()) != 0 {
		t.Fatal("empty replacement groups should be explicit", err)
	}
}
func TestParametersBoundGroupCountAfterAddingWheel(t *testing.T) {
	groups := []string{}
	for n := 0; n < 64; n++ {
		groups = append(groups, fmt.Sprintf("g%d", n))
	}
	resolver := func(string) (uint32, error) { return 100, nil }
	_, err := sealParameters(Mutation{Method: UpdateUser, FullName: "valid", Groups: groups, IsAdmin: true}, resolver)
	assertCode(t, err, InvalidParameters)
	groups[0] = "wheel"
	p, err := sealParameters(Mutation{Method: UpdateUser, FullName: "valid", Groups: groups, IsAdmin: true}, resolver)
	if err != nil || len(p.Groups()) != 64 {
		t.Fatal(err)
	}
	_, err = sealParameters(Mutation{Method: UpdateUser, FullName: "valid", Groups: append(groups, "extra")}, resolver)
	assertCode(t, err, InvalidParameters)
}
func TestParametersASCIIReferenceDigest(t *testing.T) {
	p, err := sealParameters(Mutation{Method: UpdateUser, FullName: "Lyra fixture $(literal)", Groups: []string{"users"}}, groupResolver)
	if err != nil {
		t.Fatal(err)
	}
	if p.Digest() != "142f6cb4e961406f5c9d50d8ea157d2d298b2f9cd49477ed0073c2f9d6dec8b0" {
		t.Fatal("ASCII reference digest differs")
	}
	if err := revalidateGroups(p, func(string) (uint32, error) { return 101, nil }); err != Conflict {
		t.Fatal("group change was not rejected", err)
	}
}
