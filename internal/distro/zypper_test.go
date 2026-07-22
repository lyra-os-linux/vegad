package distro

import "testing"

// zypperRejectedKeyBlock is a synthetic but format-accurate rendition of
// what `zypper --non-interactive refresh` prints (and rejects) when a
// repo's metadata is signed by a key it doesn't already trust.
const zypperRejectedKeyBlock = `
New repository or package signing key received:

  Repository:       home_rodrigosbrito_vega
  Key Name:         Rodrigo Brito <rodrigo@w3ti.com.br>
  Key Fingerprint:  AB12 CD34 EF56 AB12 CD34  EF56 AB12 CD34 EF56 AB12
  Key Created:      Tue Jul 22 00:00:00 2026
  Key Expires:      (does not expire)
  Rpm Name:         gpg-pubkey-ab12cd34-abcdef01

    (r) Reject
    (t) Trust temporarily
    (a) Trust always
    (?) Print more information

Automatically rejecting the request since zypper is running non-interactively.
`

func TestParseZypperUntrustedKey(t *testing.T) {
	keyErr, ok := parseZypperUntrustedKey("home_rodrigosbrito_vega", zypperRejectedKeyBlock)
	if !ok {
		t.Fatalf("expected parseZypperUntrustedKey to recognize the block")
	}
	if keyErr.Repo != "home_rodrigosbrito_vega" {
		t.Errorf("Repo = %q", keyErr.Repo)
	}
	const wantFingerprint = "AB12 CD34 EF56 AB12 CD34  EF56 AB12 CD34 EF56 AB12"
	if keyErr.Fingerprint != wantFingerprint {
		t.Errorf("Fingerprint = %q, want %q", keyErr.Fingerprint, wantFingerprint)
	}
	if keyErr.UserId != "Rodrigo Brito <rodrigo@w3ti.com.br>" {
		t.Errorf("UserId = %q", keyErr.UserId)
	}
	if keyErr.KeyId != "AB12" {
		t.Errorf("KeyId = %q, want the last fingerprint group", keyErr.KeyId)
	}
}

func TestParseZypperUntrustedKeyUnrelatedError(t *testing.T) {
	_, ok := parseZypperUntrustedKey("repo", "curl error 6: Could not resolve host: example.invalid")
	if ok {
		t.Fatalf("expected parseZypperUntrustedKey to reject an unrelated error")
	}
}
