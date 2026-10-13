package parental

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func issuerSigningFixture(t *testing.T) (string, string, string) {
	t.Helper()
	for _, tool := range []string{"gpg", "gpgv"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("required issuer test tool %s is unavailable: %v", tool, err)
		}
	}
	home := filepath.Join(t.TempDir(), "gnupg")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"--batch", "--no-tty", "--homedir", home, "--passphrase", "", "--quick-generate-key", "Lyra parental fixture", "ed25519", "sign", "0"}
	if out, err := exec.Command("gpg", args...).CombinedOutput(); err != nil {
		t.Fatalf("generate fixture key: %v: %s", err, out)
	}
	listing, err := exec.Command("gpg", "--batch", "--no-tty", "--homedir", home, "--with-colons", "--list-keys").Output()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := ""
	for _, line := range strings.Split(string(listing), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) > 9 && fields[0] == "fpr" {
			fingerprint = strings.ToUpper(fields[9])
			break
		}
	}
	if !issuerFingerprint.MatchString(fingerprint) {
		t.Fatalf("invalid fixture fingerprint %q", fingerprint)
	}
	keyring := filepath.Join(t.TempDir(), "fixture-keyring.gpg")
	key, err := exec.Command("gpg", "--batch", "--no-tty", "--homedir", home, "--export", fingerprint).Output()
	if err != nil || len(key) == 0 {
		t.Fatalf("export fixture public key: %v", err)
	}
	if err = os.WriteFile(keyring, key, 0600); err != nil {
		t.Fatal(err)
	}
	return home, keyring, fingerprint
}

func signIssuerBytes(t *testing.T, home, fingerprint string, data []byte) []byte {
	t.Helper()
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "envelope.json")
	sigPath := filepath.Join(dir, "envelope.json.asc")
	if err := os.WriteFile(dataPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--batch", "--no-tty", "--homedir", home, "--local-user", fingerprint, "--armor", "--detach-sign", "--output", sigPath, "--", dataPath}
	if out, err := exec.Command("gpg", args...).CombinedOutput(); err != nil {
		t.Fatalf("sign issuer fixture: %v: %s", err, out)
	}
	signature, err := os.ReadFile(sigPath)
	if err != nil {
		t.Fatal(err)
	}
	return signature
}

func signedIssuerEnvelope(t *testing.T, fingerprint, issuer, installation, operation string) []byte {
	t.Helper()
	ref := decodeReference(t, referenceRow("first-install"))
	reference, err := referenceBytes(ref)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{
		"schema": 1, "issuer": issuer, "key_fingerprint": fingerprint,
		"installation": installation, "operation": operation,
		"reference": json.RawMessage(reference),
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSignedFirstReferenceVerifierPinsSignatureAndLifecycleTuple(t *testing.T) {
	home, keyring, fingerprint := issuerSigningFixture(t)
	issuer, installation, operation := strings.Repeat("a", 32), strings.Repeat("1", 32), strings.Repeat("2", 32)
	verifier := NewSignedFirstReferenceVerifier(issuer, fingerprint, keyring)
	data := signedIssuerEnvelope(t, fingerprint, issuer, installation, operation)
	signature := signIssuerBytes(t, home, fingerprint, data)
	got, err := verifier.Verify(data, signature, installation, operation)
	if err != nil || got.Issuer() != issuer || got.Fingerprint() != fingerprint || got.Operation() != operation || got.Reference().Installation() != installation || got.Reference().Mode() != "first-install" || got.Reference().Sequence() != 1 {
		t.Fatalf("verified result = %#v, %v", got, err)
	}
	if _, err = verifier.Verify(append(append([]byte(nil), data...), ' '), signature, installation, operation); err == nil {
		t.Fatal("modified signed bytes were accepted")
	}
	if _, err = verifier.Verify(data, signature, installation, strings.Repeat("3", 32)); err == nil {
		t.Fatal("wrong requested ESP operation was accepted")
	}
	if _, err = verifier.Verify(data, []byte("not a detached signature"), installation, operation); err == nil {
		t.Fatal("invalid detached signature was accepted")
	}
	wrongPin := NewSignedFirstReferenceVerifier(issuer, strings.Repeat("B", 40), keyring)
	if _, err = wrongPin.Verify(data, signature, installation, operation); err == nil {
		t.Fatal("signature from a different primary-key fingerprint was accepted")
	}
	symlink := filepath.Join(t.TempDir(), "keyring-link.gpg")
	if err = os.Symlink(keyring, symlink); err != nil {
		t.Fatal(err)
	}
	linkedKeyring := NewSignedFirstReferenceVerifier(issuer, fingerprint, symlink)
	if _, err = linkedKeyring.Verify(data, signature, installation, operation); err == nil {
		t.Fatal("symlinked keyring was accepted")
	}
	if err = os.Chmod(keyring, 0664); err != nil {
		t.Fatal(err)
	}
	groupWritable := NewSignedFirstReferenceVerifier(issuer, fingerprint, keyring)
	if _, err = groupWritable.Verify(data, signature, installation, operation); err == nil {
		t.Fatal("group-writable keyring was accepted")
	}
}

func TestSignedFirstReferenceVerifierValidatesPinnedKeyringBeforeRequests(t *testing.T) {
	_, keyring, fingerprint := issuerSigningFixture(t)
	verifier := NewSignedFirstReferenceVerifier(strings.Repeat("a", 32), fingerprint, keyring)
	if err := verifier.ValidateKeyring(); err != nil {
		t.Fatalf("valid pinned keyring rejected: %v", err)
	}
	wrongPin := NewSignedFirstReferenceVerifier(strings.Repeat("a", 32), strings.Repeat("B", 40), keyring)
	if err := wrongPin.ValidateKeyring(); err == nil {
		t.Fatal("keyring without the configured primary fingerprint was accepted")
	}
	if err := os.Chmod(keyring, 0664); err != nil {
		t.Fatal(err)
	}
	unsafe := NewSignedFirstReferenceVerifier(strings.Repeat("a", 32), fingerprint, keyring)
	if err := unsafe.ValidateKeyring(); err == nil {
		t.Fatal("group-writable keyring was accepted")
	}
}

func TestSignedFirstReferenceEnvelopeRejectsUnsignedClaimsAndMalformedSchema(t *testing.T) {
	home, keyring, fingerprint := issuerSigningFixture(t)
	issuer, installation, operation := strings.Repeat("a", 32), strings.Repeat("1", 32), strings.Repeat("2", 32)
	verifier := NewSignedFirstReferenceVerifier(issuer, fingerprint, keyring)
	valid := signedIssuerEnvelope(t, fingerprint, issuer, installation, operation)
	for name, mutate := range map[string]func(map[string]any){
		"wrong issuer":       func(row map[string]any) { row["issuer"] = strings.Repeat("b", 32) },
		"wrong fingerprint":  func(row map[string]any) { row["key_fingerprint"] = strings.Repeat("C", 40) },
		"wrong installation": func(row map[string]any) { row["installation"] = strings.Repeat("3", 32) },
		"wrong operation":    func(row map[string]any) { row["operation"] = strings.Repeat("4", 32) },
		"extra field":        func(row map[string]any) { row["client_authority"] = true },
	} {
		t.Run(name, func(t *testing.T) {
			var row map[string]any
			if err := json.Unmarshal(valid, &row); err != nil {
				t.Fatal(err)
			}
			mutate(row)
			data, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			signature := signIssuerBytes(t, home, fingerprint, data)
			if _, err = verifier.Verify(data, signature, installation, operation); err == nil {
				t.Fatal("invalid signed claim was accepted")
			}
		})
	}
	duplicate := bytes.Replace(valid, []byte(`"schema":1`), []byte(`"schema":1,"schema":1`), 1)
	if bytes.Equal(duplicate, valid) {
		t.Fatal("fixture schema field not found")
	}
	if _, err := verifier.Verify(duplicate, signIssuerBytes(t, home, fingerprint, duplicate), installation, operation); err == nil {
		t.Fatal("duplicate JSON field was accepted")
	}
}
