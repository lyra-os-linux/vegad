package parental

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func approvalFixture(t *testing.T, entries []any) ([]byte, string, FlatpakPeerBinding) {
	t.Helper()
	if entries == nil {
		entries = []any{}
	}
	binding := FlatpakPeerBinding{Association: strings.Repeat("a", 32),
		Generation: strings.Repeat("b", 32), UID: 1003}
	row := map[string]any{"schema": 1, "association": binding.Association,
		"generation": binding.Generation, "uid": binding.UID, "approvals": entries}
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), binding
}

func approvalEntry() map[string]any {
	return map[string]any{"ref": "app/org.lyra.TestApp/x86_64/stable", "scope": "system",
		"installation": "system", "origin": "lyra-test", "publisher_fingerprint": "",
		"commit": strings.Repeat("c", 64)}
}

func TestFlatpakApprovalPolicyExplicitAndDigestBound(t *testing.T) {
	data, digest, binding := approvalFixture(t, nil)
	p, err := DecodeFlatpakApprovalPolicy(data, binding, digest)
	if err != nil || p.Digest() != digest {
		t.Fatalf("empty valid policy: %v", err)
	}
	if _, err := p.Approved(FlatpakLaunchRequest{AppID: "org.lyra.TestApp"}, binding); !errors.Is(err, Blocked) {
		t.Fatalf("zero-app policy admitted app: %v", err)
	}
	data, digest, binding = approvalFixture(t, []any{approvalEntry()})
	p, err = DecodeFlatpakApprovalPolicy(data, binding, digest)
	if err != nil {
		t.Fatal(err)
	}
	a, err := p.Approved(FlatpakLaunchRequest{AppID: "org.lyra.TestApp"}, binding)
	if err != nil || a.Ref != "app/org.lyra.TestApp/x86_64/stable" ||
		a.Commit != strings.Repeat("c", 64) || a.OwnerUID != 0 {
		t.Fatalf("approval mismatch: %+v %v", a, err)
	}
	if _, err := p.Approved(FlatpakLaunchRequest{AppID: "org.lyra.Other"}, binding); !errors.Is(err, Blocked) {
		t.Fatalf("other app admitted: %v", err)
	}
	stale := binding
	stale.Generation = strings.Repeat("f", 32)
	if _, err := p.Approved(FlatpakLaunchRequest{AppID: "org.lyra.TestApp"}, stale); !errors.Is(err, Blocked) {
		t.Fatalf("stale generation admitted: %v", err)
	}
	if _, err := DecodeFlatpakApprovalPolicy(data, binding, strings.Repeat("f", 64)); !errors.Is(err, InvalidFlatpakApprovalPolicy) {
		t.Fatalf("digest mismatch accepted: %v", err)
	}
	signed := approvalEntry()
	signed["scope"], signed["installation"] = "user", "user"
	signed["publisher_fingerprint"], signed["commit"] = strings.Repeat("d", 40), ""
	data, digest, binding = approvalFixture(t, []any{signed})
	p, err = DecodeFlatpakApprovalPolicy(data, binding, digest)
	if err != nil {
		t.Fatal(err)
	}
	a, err = p.Approved(FlatpakLaunchRequest{AppID: "org.lyra.TestApp"}, binding)
	if err != nil || a.OwnerUID != binding.UID || a.PublisherFingerprint != strings.Repeat("d", 40) {
		t.Fatalf("signed user approval mismatch: %+v %v", a, err)
	}
}

func TestFlatpakApprovalPolicyRejectsAmbiguousOrForgedRows(t *testing.T) {
	base := approvalEntry()
	for name, entries := range map[string][]any{
		"duplicate-id": {base, map[string]any{"ref": "app/org.lyra.TestApp/x86_64/beta", "scope": "system",
			"installation": "system", "origin": "lyra-test", "publisher_fingerprint": "", "commit": strings.Repeat("d", 64)}},
		"wrong-origin":         {func() any { a := approvalEntry(); a["origin"] = "--app-fd=3"; return a }()},
		"unsigned-without-pin": {func() any { a := approvalEntry(); a["commit"] = ""; return a }()},
		"unknown-field":        {func() any { a := approvalEntry(); a["uid"] = 0; return a }()},
		"invalid-scope":        {func() any { a := approvalEntry(); a["scope"] = "network"; return a }()},
	} {
		t.Run(name, func(t *testing.T) {
			data, digest, binding := approvalFixture(t, entries)
			if _, err := DecodeFlatpakApprovalPolicy(data, binding, digest); !errors.Is(err, InvalidFlatpakApprovalPolicy) {
				t.Fatalf("invalid policy accepted: %v", err)
			}
		})
	}
	_, _, binding := approvalFixture(t, nil)
	duplicate := []byte(`{"schema":1,"schema":1,"association":"` + binding.Association +
		`","generation":"` + binding.Generation + `","uid":1003,"approvals":[]}`)
	sum := sha256.Sum256(duplicate)
	if _, err := DecodeFlatpakApprovalPolicy(duplicate, binding, hex.EncodeToString(sum[:])); !errors.Is(err, InvalidFlatpakApprovalPolicy) {
		t.Fatalf("duplicate JSON key accepted: %v", err)
	}
	badUTF8 := append([]byte(`{"schema":1,"association":"`), 0xff)
	if _, err := DecodeFlatpakApprovalPolicy(badUTF8, binding, strings.Repeat("a", 64)); !errors.Is(err, InvalidFlatpakApprovalPolicy) {
		t.Fatalf("invalid UTF-8 accepted: %v", err)
	}
}
