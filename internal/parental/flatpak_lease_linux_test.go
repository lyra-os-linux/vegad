package parental

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func supervisedLeaseFixture(t *testing.T) (*fileFixture, FlatpakLaunchPeer, FlatpakPeerBinding,
	func(Account) (string, error)) {
	t.Helper()
	x := newFileFixture(t)
	row := supervisedRow()
	policy := map[string]any{"schema": 1, "association": first(row)["association"],
		"generation": first(row)["generation"], "uid": 1002,
		"approvals": []any{approvalEntry()}}
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	first(row)["adapters"].(map[string]any)["flatpak"] = hex.EncodeToString(sum[:])
	writePrivate(t, filepath.Join(x.root, "catalog", "catalog.json"), encodeRow(t, row))
	writePrivate(t, filepath.Join(x.root, "inventory", "1002.flatpak.json"), data)
	c := decodeRow(t, row)
	context := "system_u:system_r:lyra_test_child_a_t:s0"
	domain := func(Account) (string, error) { return context, nil }
	peer := FlatpakLaunchPeer{PID: 123, UID: 1002, GID: 100, SELinuxContext: context}
	binding, err := BindFlatpakLaunchPeer(c, peer, observe, []string{"pam", "flatpak"}, applied, domain)
	if err != nil {
		t.Fatal(err)
	}
	return x, peer, binding, domain
}

func TestFlatpakLeaseHoldsLocksAndRevalidates(t *testing.T) {
	x, peer, binding, domain := supervisedLeaseFixture(t)
	before := filesImage(t, x.root)
	called := 0
	err := x.backend.WithFlatpakLease(binding, peer, FlatpakLaunchRequest{AppID: "org.lyra.TestApp"}, observe, []string{"pam", "flatpak"}, applied, domain,
		func(fresh FlatpakPeerBinding, approval FlatpakApproval) error {
			called++
			if fresh != binding || approval.Ref != "app/org.lyra.TestApp/x86_64/stable" {
				t.Fatal("binding changed under lease")
			}
			for _, lock := range x.locks {
				probe(t, lock, unix.LOCK_EX, true)
				probe(t, lock, unix.LOCK_SH, false)
			}
			return nil
		})
	if err != nil || called != 1 {
		t.Fatal(err, called)
	}
	assertReleased(t, x)
	if after := filesImage(t, x.root); len(before) != len(after) {
		t.Fatal("lease wrote state")
	} else {
		for path, image := range before {
			if after[path] != image {
				t.Fatal("lease changed file", path)
			}
		}
	}
}

func TestFlatpakLeaseDeniesAppOutsideExplicitList(t *testing.T) {
	x, peer, binding, domain := supervisedLeaseFixture(t)
	called := false
	err := x.backend.WithFlatpakLease(binding, peer, FlatpakLaunchRequest{AppID: "org.lyra.Other"},
		observe, []string{"pam", "flatpak"}, applied, domain,
		func(FlatpakPeerBinding, FlatpakApproval) error { called = true; return nil })
	if !errors.Is(err, Blocked) || called {
		t.Fatalf("unapproved app admitted: %v", err)
	}
	assertReleased(t, x)
}

func TestFlatpakLeaseDeniesChangedStateBeforeVisitor(t *testing.T) {
	for name, change := range map[string]func(*testing.T, *fileFixture){
		"catalog-generation": func(t *testing.T, x *fileFixture) {
			row := supervisedRow()
			first(row)["generation"] = strings.Repeat("f", 32)
			writePrivate(t, filepath.Join(x.root, "catalog", "catalog.json"), encodeRow(t, row))
		},
		"missing-approval-policy": func(t *testing.T, x *fileFixture) {
			if err := os.Remove(filepath.Join(x.root, "inventory", "1002.flatpak.json")); err != nil {
				t.Fatal(err)
			}
		},
		"tampered-approval-policy": func(t *testing.T, x *fileFixture) {
			writePrivate(t, filepath.Join(x.root, "inventory", "1002.flatpak.json"), []byte(`{"schema":1,"approvals":[]}`))
		},
		"negative-marker": func(t *testing.T, x *fileFixture) {
			writePrivate(t, filepath.Join(x.root, "ledger", ".deny-1002"), []byte("deny"))
		},
		"bad-proof": func(t *testing.T, x *fileFixture) {
			writePrivate(t, filepath.Join(x.root, "ledger", "1002"), []byte("not a proof"))
		},
		"missing-inventory": func(t *testing.T, x *fileFixture) {
			if err := os.Remove(filepath.Join(x.root, "inventory", "1002.json")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			x, peer, binding, domain := supervisedLeaseFixture(t)
			change(t, x)
			called := false
			err := x.backend.WithFlatpakLease(binding, peer, FlatpakLaunchRequest{AppID: "org.lyra.TestApp"}, observe, []string{"pam", "flatpak"}, applied, domain,
				func(FlatpakPeerBinding, FlatpakApproval) error { called = true; return nil })
			if err == nil || called {
				t.Fatalf("changed state admitted: %v", err)
			}
			assertReleased(t, x)
		})
	}
	x, peer, binding, domain := supervisedLeaseFixture(t)
	called := false
	err := x.backend.WithFlatpakLease(binding, peer, FlatpakLaunchRequest{AppID: "org.lyra.TestApp"}, observe, []string{"pam", "flatpak"},
		func(a Account) (Applied, error) {
			v, _ := applied(a)
			v.Generation = strings.Repeat("f", 32)
			return v, nil
		}, domain, func(FlatpakPeerBinding, FlatpakApproval) error { called = true; return nil })
	if !errors.Is(err, UnsupportedProducer) || called {
		t.Fatalf("stale adapter admitted: %v", err)
	}
	assertReleased(t, x)
}
