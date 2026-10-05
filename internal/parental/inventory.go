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

const InvalidInventory Code = "InvalidInventory"

var requiredProducers = []string{"classification", "cron", "dbus-activation", "gdm", "ssh", "system-units", "user-manager"}
var routeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// registeredInventory validates the laboratory's registered inventory schema.
// A valid file is not a certificate of namespace coverage or enforcement.
func registeredInventory(data []byte, expected Identity) (string, error) {
	if len(data) == 0 || len(data) > CatalogLimit || !utf8.Valid(data) {
		return "", InvalidInventory
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := readJSON(d, 0)
	if err != nil {
		return "", InvalidInventory
	}
	if _, err = d.Token(); err != io.EOF {
		return "", InvalidInventory
	}
	row, ok := object(v, "schema", "identity", "revision", "producers")
	if !ok {
		return "", InvalidInventory
	}
	if schema, ok := positive(row["schema"], 1); !ok || schema != 1 {
		return "", InvalidInventory
	}
	identity, ok := parseIdentity(row["identity"])
	if !ok {
		return "", InvalidInventory
	}
	if identity != expected {
		return "", InvalidIdentity
	}
	if _, ok = positive(row["revision"], math.MaxInt64); !ok {
		return "", InvalidInventory
	}
	producers, ok := row["producers"].(map[string]any)
	if !ok {
		return "", InvalidInventory
	}
	if len(producers) != len(requiredProducers) {
		return "", UnsupportedProducer
	}
	for _, name := range requiredProducers {
		raw, exists := producers[name]
		if !exists {
			return "", UnsupportedProducer
		}
		item, ok := object(raw, "state", "adapter_digest", "routes")
		if !ok {
			return "", InvalidInventory
		}
		if state, ok := item["state"].(string); !ok || state != "registered" {
			return "", UnsupportedProducer
		}
		if _, ok = matching(item["adapter_digest"], hash); !ok {
			return "", InvalidInventory
		}
		routes, ok := item["routes"].([]any)
		if !ok || len(routes) == 0 || len(routes) > 64 {
			return "", InvalidInventory
		}
		seen := map[string]bool{}
		for _, raw := range routes {
			route, ok := object(raw, "id", "digest")
			if !ok {
				return "", InvalidInventory
			}
			id, ok := matching(route["id"], routeName)
			if !ok || seen[id] {
				return "", InvalidInventory
			}
			seen[id] = true
			if _, ok = matching(route["digest"], hash); !ok {
				return "", InvalidInventory
			}
		}
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		return "", InvalidInventory
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
