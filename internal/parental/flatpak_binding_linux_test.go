package parental

import (
	"errors"
	"strings"
	"testing"
)

func TestFlatpakPeerBindingRequiresActiveSupervisedAccount(t *testing.T) {
	row := supervisedRow()
	a := first(row)
	a["identity"] = map[string]any{"user": "childa", "uid": 1003, "gid": 100, "group": "users"}
	second := map[string]any{
		"association": strings.Repeat("f", 32), "generation": strings.Repeat("9", 32),
		"identity": map[string]any{"user": "childb", "uid": 1011, "gid": 101, "group": "users"},
		"revision": 1, "kind": "supervised", "state": "active", "operation": "", "barrier": false,
		"policy":   strings.Repeat("7", 64),
		"adapters": map[string]any{"pam": strings.Repeat("8", 64), "flatpak": strings.Repeat("6", 64)},
	}
	row["accounts"] = append(row["accounts"].([]any), second)
	c := decodeRow(t, row)
	identity := func(uid uint32) (Identity, error) {
		for _, account := range c.Accounts() {
			if account.Identity.UID == uid {
				return account.Identity, nil
			}
		}
		return Identity{}, errors.New("missing NSS entry")
	}
	domain := func(a Account) (string, error) {
		if a.Identity.UID == 1003 {
			return "system_u:system_r:lyra_test_child_a_t:s0", nil
		}
		return "system_u:system_r:lyra_test_child_b_t:s0", nil
	}
	for _, tc := range []struct {
		uid, gid uint32
		context  string
	}{
		{1003, 100, "system_u:system_r:lyra_test_child_a_t:s0"},
		{1011, 101, "system_u:system_r:lyra_test_child_b_t:s0"},
	} {
		peer := FlatpakLaunchPeer{PID: 123, UID: tc.uid, GID: tc.gid, SELinuxContext: tc.context}
		bound, err := BindFlatpakLaunchPeer(c, peer, identity, []string{"pam", "flatpak"}, applied, domain)
		expectedGeneration := strings.Repeat("b", 32)
		if tc.uid == 1011 {
			expectedGeneration = strings.Repeat("9", 32)
		}
		if err != nil || bound.UID != tc.uid || bound.GID != tc.gid || bound.SELinuxContext != tc.context ||
			bound.CatalogDigest != c.Digest() || bound.CatalogRevision != c.Revision() || bound.Generation != expectedGeneration {
			t.Fatalf("binding failed: %+v %v", bound, err)
		}
		for _, bad := range []FlatpakLaunchPeer{
			{PID: 123, UID: tc.uid, GID: tc.gid + 1, SELinuxContext: tc.context},
			{PID: 123, UID: tc.uid, GID: tc.gid, SELinuxContext: "system_u:system_r:unconfined_service_t:s0"},
			{PID: 123, UID: tc.uid, GID: tc.gid, SELinuxContext: "kernel"},
		} {
			if _, err := BindFlatpakLaunchPeer(c, bad, identity, []string{"pam", "flatpak"}, applied, domain); err == nil {
				t.Fatalf("accepted mismatched peer: %+v", bad)
			}
		}
	}
	if _, err := BindFlatpakLaunchPeer(c, FlatpakLaunchPeer{PID: 123, UID: 1003, GID: 100, SELinuxContext: "system_u:system_r:lyra_test_child_a_t:s0"},
		identity, []string{"pam", "flatpak"}, func(a Account) (Applied, error) {
			v, _ := applied(a)
			v.Generation = strings.Repeat("0", 32)
			return v, nil
		}, domain); !errors.Is(err, UnsupportedProducer) {
		t.Fatalf("stale generation accepted: %v", err)
	}
	blocked := supervisedRow()
	first(blocked)["barrier"] = true
	first(blocked)["operation"] = strings.Repeat("9", 32)
	if _, err := BindFlatpakLaunchPeer(decodeRow(t, blocked), FlatpakLaunchPeer{PID: 123, UID: 1002, GID: 100, SELinuxContext: "system_u:system_r:lyra_test_child_a_t:s0"},
		observe, []string{"pam", "flatpak"}, applied, domain); !errors.Is(err, Blocked) {
		t.Fatalf("barrier ignored: %v", err)
	}
	ordinary := decodeRow(t, fixtureRow())
	if _, err := BindFlatpakLaunchPeer(ordinary, FlatpakLaunchPeer{PID: 123, UID: 1002, GID: 100, SELinuxContext: "system_u:system_r:lyra_test_child_a_t:s0"},
		observe, []string{"pam", "flatpak"}, applied, domain); !errors.Is(err, Blocked) {
		t.Fatalf("ordinary account admitted: %v", err)
	}
}
