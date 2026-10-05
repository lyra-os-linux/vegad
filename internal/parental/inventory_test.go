package parental

import (
	"bytes"
	"strings"
	"testing"
)

func TestRegisteredInventoryStrictSchema(t *testing.T) {
	id, _ := observe(1002)
	changes := map[string]func(map[string]any){
		"unknown field":    func(r map[string]any) { r["complete"] = true },
		"bool schema":      func(r map[string]any) { r["schema"] = true },
		"decimal revision": func(r map[string]any) { r["revision"] = 1.1 },
		"null revision":    func(r map[string]any) { r["revision"] = nil },
		"identity changed": func(r map[string]any) { r["identity"].(map[string]any)["uid"] = 1000 },
		"missing producer": func(r map[string]any) { delete(r["producers"].(map[string]any), "cron") },
		"unknown producer": func(r map[string]any) { r["producers"].(map[string]any)["extra"] = map[string]any{} },
		"unsupported producer": func(r map[string]any) {
			r["producers"].(map[string]any)["cron"].(map[string]any)["state"] = "unsupported"
		},
		"claimed complete": func(r map[string]any) { r["producers"].(map[string]any)["cron"].(map[string]any)["state"] = "complete" },
		"bad adapter": func(r map[string]any) {
			r["producers"].(map[string]any)["cron"].(map[string]any)["adapter_digest"] = "bad"
		},
		"empty routes": func(r map[string]any) { r["producers"].(map[string]any)["cron"].(map[string]any)["routes"] = []any{} },
		"unknown route field": func(r map[string]any) {
			routes := r["producers"].(map[string]any)["cron"].(map[string]any)["routes"].([]any)
			routes[0].(map[string]any)["path"] = "/tmp/invalid"
		},
		"duplicate route": func(r map[string]any) {
			p := r["producers"].(map[string]any)["cron"].(map[string]any)
			routes := p["routes"].([]any)
			p["routes"] = append(routes, routes[0])
		},
		"too many routes": func(r map[string]any) {
			r["producers"].(map[string]any)["cron"].(map[string]any)["routes"] = make([]any, 65)
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			row := inventoryRow(id)
			change(row)
			_, err := registeredInventory(encodeRow(t, row), id)
			want := InvalidInventory
			switch name {
			case "identity changed":
				want = InvalidIdentity
			case "missing producer", "unknown producer", "unsupported producer", "claimed complete":
				want = UnsupportedProducer
			}
			assertCode(t, err, want)
		})
	}
	good := encodeRow(t, inventoryRow(id))
	for _, data := range [][]byte{nil, bytes.Repeat([]byte(" "), CatalogLimit+1), append(append([]byte(nil), good...), good...), bytes.Replace(good, []byte(`"schema":1`), []byte(`"schema":1,"schema":1`), 1), {0xff}} {
		_, err := registeredInventory(data, id)
		assertCode(t, err, InvalidInventory)
	}
}
func TestRegisteredInventoryDigestBindsRevisionAndRoutes(t *testing.T) {
	id, _ := observe(1002)
	row := inventoryRow(id)
	first, err := registeredInventory(encodeRow(t, row), id)
	if err != nil {
		t.Fatal(err)
	}
	row["revision"] = 2
	second, err := registeredInventory(encodeRow(t, row), id)
	if err != nil || first == second {
		t.Fatal("revision not sealed", err)
	}
	p := row["producers"].(map[string]any)["cron"].(map[string]any)
	p["routes"].([]any)[0].(map[string]any)["digest"] = strings.Repeat("e", 64)
	third, err := registeredInventory(encodeRow(t, row), id)
	if err != nil || second == third {
		t.Fatal("routes not sealed", err)
	}
}
