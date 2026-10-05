// Package parental contains the read-only foundation for managed accounts.
// It is not wired into UsersService: bootstrap and recovery must be qualified
// before absence of a catalog may affect existing accounts.
package parental

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"regexp"
	"unicode/utf8"
)

const CatalogLimit = 65536
const maxAccounts = 128

// Code is safe to return without exposing catalog data or filesystem paths.
type Code string

func (c Code) Error() string { return string(c) }

const (
	InvalidCatalog      Code = "InvalidCatalog"
	UnsafeCatalog       Code = "UnsafeCatalog"
	RecoveryRequired    Code = "RecoveryRequired"
	Busy                Code = "Busy"
	InvalidIdentity     Code = "InvalidIdentity"
	Blocked             Code = "Blocked"
	UnsupportedProducer Code = "UnsupportedProducer"
)

var accountName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,62}$`)
var token = regexp.MustCompile(`^[0-9a-f]{32}$`)
var hash = regexp.MustCompile(`^[0-9a-f]{64}$`)
var integer = regexp.MustCompile(`^[1-9][0-9]*$`)

// Identity is the complete local identity, not a client-supplied UID claim.
type Identity struct {
	User  string `json:"user"`
	UID   uint32 `json:"uid"`
	GID   uint32 `json:"gid"`
	Group string `json:"group"`
}
type Adapter struct{ Name, Digest string }
type Account struct {
	Association, Generation string
	Identity                Identity
	Revision                uint64
	Kind, State, Operation  string
	Barrier                 bool
	Policy                  string
	Adapters                []Adapter
}

// Catalog owns its accounts. Accessors return copies including nested slices.
// Digest detects changes in the local snapshot; it is not anti-rollback proof.
type Catalog struct {
	revision uint64
	digest   string
	accounts []Account
}

func (c Catalog) Revision() uint64   { return c.revision }
func (c Catalog) Digest() string     { return c.digest }
func cloneAccount(a Account) Account { a.Adapters = append([]Adapter(nil), a.Adapters...); return a }
func (c Catalog) Accounts() []Account {
	out := make([]Account, len(c.accounts))
	for n, a := range c.accounts {
		out[n] = cloneAccount(a)
	}
	return out
}

// readJSON rejects duplicates at every level before schema validation. Go's
// default Unmarshal accepts duplicates and nulls; neither is a valid field here.
func readJSON(d *json.Decoder, depth int) (any, error) {
	if depth > 32 {
		return nil, InvalidCatalog
	}
	t, err := d.Token()
	if err != nil {
		return nil, InvalidCatalog
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return t, nil
	}
	switch delim {
	case '{':
		out := map[string]any{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, InvalidCatalog
			}
			key, ok := k.(string)
			if !ok {
				return nil, InvalidCatalog
			}
			if _, exists := out[key]; exists {
				return nil, InvalidCatalog
			}
			v, err := readJSON(d, depth+1)
			if err != nil {
				return nil, err
			}
			out[key] = v
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, InvalidCatalog
		}
		return out, nil
	case '[':
		out := []any{}
		for d.More() {
			v, err := readJSON(d, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, InvalidCatalog
		}
		return out, nil
	default:
		return nil, InvalidCatalog
	}
}
func object(v any, keys ...string) (map[string]any, bool) {
	row, ok := v.(map[string]any)
	if !ok || len(row) != len(keys) {
		return nil, false
	}
	for _, k := range keys {
		if _, exists := row[k]; !exists {
			return nil, false
		}
	}
	return row, true
}
func positive(v any, max uint64) (uint64, bool) {
	n, ok := v.(json.Number)
	if !ok || !integer.MatchString(n.String()) {
		return 0, false
	}
	value, err := n.Int64()
	return uint64(value), err == nil && value > 0 && uint64(value) <= max
}
func matching(v any, p *regexp.Regexp) (string, bool) {
	s, ok := v.(string)
	return s, ok && p.MatchString(s)
}
func oneOf(v any, values ...string) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	for _, allowed := range values {
		if s == allowed {
			return s, true
		}
	}
	return s, false
}
func parseIdentity(v any) (Identity, bool) {
	row, ok := object(v, "user", "uid", "gid", "group")
	if !ok {
		return Identity{}, false
	}
	user, a := matching(row["user"], accountName)
	group, b := matching(row["group"], accountName)
	uid, c := positive(row["uid"], math.MaxUint32-1)
	gid, d := positive(row["gid"], math.MaxUint32-1)
	return Identity{user, uint32(uid), uint32(gid), group}, a && b && c && d
}
func parseAccount(v any, revision uint64) (Account, error) {
	r, ok := object(v, "association", "generation", "identity", "revision", "kind", "state", "operation", "barrier", "policy", "adapters")
	if !ok {
		return Account{}, InvalidCatalog
	}
	a := Account{}
	var valid bool
	if a.Association, valid = matching(r["association"], token); !valid {
		return a, InvalidCatalog
	}
	if a.Generation, valid = matching(r["generation"], token); !valid {
		return a, InvalidCatalog
	}
	if a.Identity, valid = parseIdentity(r["identity"]); !valid {
		return a, InvalidCatalog
	}
	if a.Revision, valid = positive(r["revision"], revision); !valid {
		return a, InvalidCatalog
	}
	if a.Kind, valid = oneOf(r["kind"], "ordinary", "supervised"); !valid {
		return a, InvalidCatalog
	}
	if a.State, valid = oneOf(r["state"], "active", "provisioning", "transitioning", "recovering", "tombstone"); !valid {
		return a, InvalidCatalog
	}
	if a.Operation, valid = r["operation"].(string); !valid || a.Operation != "" && !token.MatchString(a.Operation) {
		return a, InvalidCatalog
	}
	if a.Barrier, valid = r["barrier"].(bool); !valid {
		return a, InvalidCatalog
	}
	if a.Policy, valid = r["policy"].(string); !valid {
		return a, InvalidCatalog
	}
	adapters, ok := r["adapters"].(map[string]any)
	if !ok || len(adapters) > 32 {
		return a, InvalidCatalog
	}
	for name, value := range adapters {
		digest, ok := matching(value, hash)
		if !accountName.MatchString(name) || !ok {
			return a, InvalidCatalog
		}
		a.Adapters = append(a.Adapters, Adapter{name, digest})
	}
	sortAdapters(a.Adapters)
	if a.Kind == "ordinary" {
		if a.Policy != "" || len(a.Adapters) != 0 {
			return a, InvalidCatalog
		}
	} else if !hash.MatchString(a.Policy) || len(a.Adapters) == 0 {
		return a, InvalidCatalog
	}
	if a.State == "active" && !a.Barrier {
		if a.Operation != "" {
			return a, InvalidCatalog
		}
	} else if !token.MatchString(a.Operation) {
		return a, InvalidCatalog
	}
	return a, nil
}

// DecodeCatalog validates the complete closed schema before exposing any account.
// Conflicting entries, including tombstones, invalidate the entire catalog.
func DecodeCatalog(data []byte) (Catalog, error) {
	if len(data) == 0 || len(data) > CatalogLimit || !utf8.Valid(data) {
		return Catalog{}, InvalidCatalog
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := readJSON(d, 0)
	if err != nil {
		return Catalog{}, err
	}
	if _, err = d.Token(); err != io.EOF {
		return Catalog{}, InvalidCatalog
	}
	row, ok := object(v, "schema", "revision", "accounts")
	if !ok {
		return Catalog{}, InvalidCatalog
	}
	if s, ok := positive(row["schema"], 1); !ok || s != 1 {
		return Catalog{}, InvalidCatalog
	}
	revision, ok := positive(row["revision"], math.MaxInt64)
	if !ok {
		return Catalog{}, InvalidCatalog
	}
	entries, ok := row["accounts"].([]any)
	if !ok || len(entries) > maxAccounts {
		return Catalog{}, InvalidCatalog
	}
	c := Catalog{revision: revision}
	associations := map[string]bool{}
	generations := map[string]bool{}
	uids := map[uint32]bool{}
	names := map[string]bool{}
	for _, entry := range entries {
		a, err := parseAccount(entry, revision)
		if err != nil {
			return Catalog{}, err
		}
		if associations[a.Association] || generations[a.Generation] || uids[a.Identity.UID] || names[a.Identity.User] {
			return Catalog{}, InvalidCatalog
		}
		associations[a.Association] = true
		generations[a.Generation] = true
		uids[a.Identity.UID] = true
		names[a.Identity.User] = true
		c.accounts = append(c.accounts, a)
	}
	canonical, err := json.Marshal(row)
	if err != nil {
		return Catalog{}, InvalidCatalog
	}
	sum := sha256.Sum256(canonical)
	c.digest = hex.EncodeToString(sum[:])
	return c, nil
}
