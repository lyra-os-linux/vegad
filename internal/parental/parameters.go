package parental

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

const InvalidParameters Code = "InvalidParameters"

type Method string

const (
	UpdateUser Method = "UpdateUser"
	SetAdmin   Method = "SetAdmin"
)

// Mutation is a typed internal input, not a published JSON or D-Bus schema.
// Password/photo/create/remove have no representation in this component.
type Mutation struct {
	Method   Method
	FullName string
	Groups   []string
	IsAdmin  bool
}
type Group struct {
	Name string `json:"name"`
	GID  uint32 `json:"gid"`
}
type Parameters struct {
	fullName string
	groups   []Group
	isAdmin  bool
}

func (p Parameters) FullName() string        { return p.fullName }
func (p Parameters) Groups() []Group         { return append([]Group(nil), p.groups...) }
func (p Parameters) IsAdmin() bool           { return p.isAdmin }
func copyParameters(p Parameters) Parameters { p.groups = p.Groups(); return p }
func (p Parameters) Digest() string {
	groups := make([]map[string]any, 0, len(p.groups))
	for _, group := range p.groups {
		groups = append(groups, map[string]any{"name": group.Name, "gid": group.GID})
	}
	data, _ := json.Marshal(map[string]any{"full_name": p.fullName, "groups": groups, "is_admin": p.isAdmin})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
func sealParameters(input Mutation, resolveGroup func(string) (uint32, error)) (Parameters, error) {
	if input.Method != UpdateUser && input.Method != SetAdmin {
		return Parameters{}, UnsupportedOperation
	}
	if resolveGroup == nil {
		return Parameters{}, RecoveryRequired
	}
	groups := map[string]bool{}
	name := ""
	if input.Method == UpdateUser {
		name = input.FullName
		if !utf8.ValidString(name) || strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 128 || len(input.Groups) > 64 {
			return Parameters{}, InvalidParameters
		}
		for _, ch := range name {
			if ch < 32 || ch == 127 || ch == ':' {
				return Parameters{}, InvalidParameters
			}
		}
		for _, group := range input.Groups {
			if !accountName.MatchString(group) || groups[group] || group == "wheel" && !input.IsAdmin {
				return Parameters{}, InvalidParameters
			}
			groups[group] = true
		}
		if input.IsAdmin {
			groups["wheel"] = true
		}
	} else {
		if input.FullName != "" || len(input.Groups) != 0 {
			return Parameters{}, InvalidParameters
		}
		groups["wheel"] = true
	}
	if len(groups) > 64 {
		return Parameters{}, InvalidParameters
	}
	names := make([]string, 0, len(groups))
	for group := range groups {
		names = append(names, group)
	}
	sort.Strings(names)
	p := Parameters{fullName: name, isAdmin: input.IsAdmin}
	for _, group := range names {
		gid, err := resolveGroup(group)
		if err != nil || gid == 0 || gid == math.MaxUint32 {
			return Parameters{}, InvalidParameters
		}
		p.groups = append(p.groups, Group{group, gid})
	}
	return p, nil
}
func revalidateGroups(params Parameters, resolveGroup func(string) (uint32, error)) error {
	if resolveGroup == nil {
		return Conflict
	}
	for _, group := range params.groups {
		gid, err := resolveGroup(group.Name)
		if err != nil || gid != group.GID {
			return Conflict
		}
	}
	return nil
}
