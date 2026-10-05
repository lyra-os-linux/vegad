package parental

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func fixtureRow() map[string]any {
	return map[string]any{
		"schema": 1, "revision": 1, "accounts": []any{map[string]any{
			"association": strings.Repeat("a", 32), "generation": strings.Repeat("b", 32),
			"identity": map[string]any{"user": "ordinaryuser", "uid": 1002, "gid": 100, "group": "users"},
			"revision": 1, "kind": "ordinary", "state": "active", "operation": "", "barrier": false, "policy": "", "adapters": map[string]any{},
		}},
	}
}
func encodeRow(t *testing.T, row map[string]any) []byte {
	t.Helper()
	out, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func first(row map[string]any) map[string]any { return row["accounts"].([]any)[0].(map[string]any) }
func decodeRow(t *testing.T, row map[string]any) Catalog {
	t.Helper()
	c, err := DecodeCatalog(encodeRow(t, row))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func assertCode(t *testing.T, err error, code Code) {
	t.Helper()
	if !errors.Is(err, code) {
		t.Fatalf("got %v, want %s", err, code)
	}
}

func TestCatalogRejectsMalformedSchema(t *testing.T) {
	cases := map[string]func(map[string]any){
		"unknown top field":            func(r map[string]any) { r["age"] = 12 },
		"missing top field":            func(r map[string]any) { delete(r, "revision") },
		"bool schema":                  func(r map[string]any) { r["schema"] = true },
		"unknown schema":               func(r map[string]any) { r["schema"] = 2 },
		"null revision":                func(r map[string]any) { r["revision"] = nil },
		"bool revision":                func(r map[string]any) { r["revision"] = true },
		"zero revision":                func(r map[string]any) { r["revision"] = 0 },
		"negative revision":            func(r map[string]any) { r["revision"] = -1 },
		"revision overflow":            func(r map[string]any) { r["revision"] = json.Number("9223372036854775808") },
		"null accounts":                func(r map[string]any) { r["accounts"] = nil },
		"unknown account field":        func(r map[string]any) { first(r)["birthdate"] = "2000" },
		"missing account field":        func(r map[string]any) { delete(first(r), "barrier") },
		"bad association":              func(r map[string]any) { first(r)["association"] = "A" },
		"bad generation":               func(r map[string]any) { first(r)["generation"] = "" },
		"future account revision":      func(r map[string]any) { first(r)["revision"] = 2 },
		"null barrier":                 func(r map[string]any) { first(r)["barrier"] = nil },
		"unknown kind":                 func(r map[string]any) { first(r)["kind"] = "adult" },
		"unknown state":                func(r map[string]any) { first(r)["state"] = "enabled" },
		"active operation":             func(r map[string]any) { first(r)["operation"] = strings.Repeat("c", 32) },
		"barrier missing operation":    func(r map[string]any) { first(r)["barrier"] = true },
		"transition missing operation": func(r map[string]any) { first(r)["state"] = "transitioning" },
		"ordinary policy":              func(r map[string]any) { first(r)["policy"] = strings.Repeat("c", 64) },
		"ordinary adapters":            func(r map[string]any) { first(r)["adapters"] = map[string]any{"pam": strings.Repeat("c", 64)} },
		"null adapters":                func(r map[string]any) { first(r)["adapters"] = nil },
		"supervised missing coverage":  func(r map[string]any) { first(r)["kind"] = "supervised"; first(r)["policy"] = strings.Repeat("c", 64) },
		"unknown identity field":       func(r map[string]any) { first(r)["identity"].(map[string]any)["shell"] = "/bin/sh" },
		"root identity":                func(r map[string]any) { first(r)["identity"].(map[string]any)["uid"] = 0 },
		"uid bool":                     func(r map[string]any) { first(r)["identity"].(map[string]any)["uid"] = true },
		"uid overflow":                 func(r map[string]any) { first(r)["identity"].(map[string]any)["uid"] = 4294967295 },
		"gid root":                     func(r map[string]any) { first(r)["identity"].(map[string]any)["gid"] = 0 },
		"invalid name":                 func(r map[string]any) { first(r)["identity"].(map[string]any)["user"] = "--help" },
		"invalid group":                func(r map[string]any) { first(r)["identity"].(map[string]any)["group"] = "users\n" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			row := fixtureRow()
			change(row)
			_, err := DecodeCatalog(encodeRow(t, row))
			assertCode(t, err, InvalidCatalog)
		})
	}
}
func TestCatalogRejectsAmbiguousJSON(t *testing.T) {
	good := encodeRow(t, fixtureRow())
	cases := map[string][]byte{
		"empty": nil, "oversize": bytes.Repeat([]byte(" "), CatalogLimit+1), "invalid utf8": {0xff},
		"trailing document":     append(append([]byte(nil), good...), good...),
		"duplicate root":        []byte(`{"schema":1,"schema":1,"revision":1,"accounts":[]}`),
		"duplicate nested":      bytes.Replace(good, []byte(`"barrier":false`), []byte(`"barrier":false,"barrier":true`), 1),
		"duplicate escaped key": []byte(`{"schema":1,"\u0073chema":1,"revision":1,"accounts":[]}`),
		"decimal":               bytes.Replace(good, []byte(`"revision":1`), []byte(`"revision":1.0`), 1),
		"exponent":              bytes.Replace(good, []byte(`"revision":1`), []byte(`"revision":1e0`), 1),
		"nan":                   []byte(`{"schema":1,"revision":NaN,"accounts":[]}`),
		"deep nesting":          []byte(strings.Repeat("[", 40) + "0" + strings.Repeat("]", 40)),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) { _, err := DecodeCatalog(data); assertCode(t, err, InvalidCatalog) })
	}
}
func TestCatalogRejectsDuplicateOwnership(t *testing.T) {
	for _, field := range []string{"association", "generation", "uid", "user"} {
		t.Run(field, func(t *testing.T) {
			row := fixtureRow()
			other := first(fixtureRow())
			other["association"] = strings.Repeat("c", 32)
			other["generation"] = strings.Repeat("d", 32)
			id := other["identity"].(map[string]any)
			id["uid"] = 1003
			id["user"] = "peer"
			if field == "uid" || field == "user" {
				id[field] = first(row)["identity"].(map[string]any)[field]
			} else {
				other[field] = first(row)[field]
			}
			row["accounts"] = append(row["accounts"].([]any), other)
			_, err := DecodeCatalog(encodeRow(t, row))
			assertCode(t, err, InvalidCatalog)
		})
	}
}
func TestCatalogLimitsAndDigest(t *testing.T) {
	row := fixtureRow()
	c := decodeRow(t, row)
	pretty, err := json.MarshalIndent(row, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	second, err := DecodeCatalog(pretty)
	if err != nil || c.Digest() != second.Digest() {
		t.Fatal("whitespace changed digest", err)
	}
	if c.Revision() != 1 || len(c.Accounts()) != 1 {
		t.Fatal("missing account")
	}
	a := c.Accounts()
	a[0].Identity.User = "mutated"
	a[0].Generation = "mutated"
	if c.Accounts()[0].Identity.User != "ordinaryuser" {
		t.Fatal("catalog changed through returned account")
	}
	row["accounts"] = []any{}
	if len(decodeRow(t, row).Accounts()) != 0 {
		t.Fatal("empty catalog")
	}
	row["accounts"] = make([]any, maxAccounts+1)
	_, err = DecodeCatalog(encodeRow(t, row))
	assertCode(t, err, InvalidCatalog)
}
func supervisedRow() map[string]any {
	r := fixtureRow()
	a := first(r)
	a["kind"] = "supervised"
	a["policy"] = strings.Repeat("c", 64)
	a["adapters"] = map[string]any{"pam": strings.Repeat("d", 64), "flatpak": strings.Repeat("e", 64)}
	return r
}
func TestCatalogSupervisedCopiesAndCoverage(t *testing.T) {
	c := decodeRow(t, supervisedRow())
	a := c.Accounts()
	a[0].Adapters[0].Digest = "mutated"
	if c.Accounts()[0].Adapters[0].Digest == "mutated" {
		t.Fatal("mutable adapter escaped")
	}
	for _, change := range []func(map[string]any){
		func(a map[string]any) { a["policy"] = "" },
		func(a map[string]any) { a["adapters"] = map[string]any{"pam": "bad"} },
		func(a map[string]any) { a["adapters"] = map[string]any{"../pam": strings.Repeat("d", 64)} },
		func(a map[string]any) {
			adapters := map[string]any{}
			for n := 0; n < 33; n++ {
				adapters["a"+strings.Repeat("b", n)] = strings.Repeat("d", 64)
			}
			a["adapters"] = adapters
		},
	} {
		row := supervisedRow()
		change(first(row))
		_, err := DecodeCatalog(encodeRow(t, row))
		assertCode(t, err, InvalidCatalog)
	}
}
