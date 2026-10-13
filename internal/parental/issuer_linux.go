package parental

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	SignedIssuerEnvelopeLimit  = ReferenceLimit + 2048
	SignedIssuerSignatureLimit = 16 * 1024
)

var issuerFingerprint = regexp.MustCompile(`^(?:[A-F0-9]{40}|[A-F0-9]{64})$`)

// SignedFirstReference is accepted only after detached-signature verification
// against a service-configured keyring and primary-key fingerprint.
type SignedFirstReference struct {
	issuer, fingerprint, operation string
	reference                      RecoveryReference
}

func (r SignedFirstReference) Issuer() string               { return r.issuer }
func (r SignedFirstReference) Fingerprint() string          { return r.fingerprint }
func (r SignedFirstReference) Operation() string            { return r.operation }
func (r SignedFirstReference) Reference() RecoveryReference { return r.reference }

// SignedFirstReferenceVerifier verifies an exact JSON byte sequence using a
// dedicated, service-owned OpenPGP keyring. The keyring path and pinned
// fingerprint must come from trusted package/service configuration; callers
// must never supply either through D-Bus. No transport or cache is implied.
type SignedFirstReferenceVerifier struct {
	issuer, fingerprint, keyring string
	timeout                      time.Duration
}

func NewSignedFirstReferenceVerifier(issuer, fingerprint, keyring string) *SignedFirstReferenceVerifier {
	return &SignedFirstReferenceVerifier{
		issuer:      issuer,
		fingerprint: strings.ToUpper(fingerprint),
		keyring:     keyring,
		timeout:     5 * time.Second,
	}
}

func (v *SignedFirstReferenceVerifier) Verify(data, signature []byte, installation, operation string) (SignedFirstReference, error) {
	if v == nil || !token.MatchString(v.issuer) || !issuerFingerprint.MatchString(v.fingerprint) ||
		!filepath.IsAbs(v.keyring) || filepath.Clean(v.keyring) != v.keyring ||
		!token.MatchString(installation) || !token.MatchString(operation) ||
		len(data) == 0 || len(data) > SignedIssuerEnvelopeLimit || len(signature) == 0 || len(signature) > SignedIssuerSignatureLimit || !utf8.Valid(data) {
		return SignedFirstReference{}, RecoveryRequired
	}
	if err := verifyIssuerSignature(v.keyring, v.fingerprint, data, signature, v.timeout); err != nil {
		return SignedFirstReference{}, Denied
	}
	return decodeSignedFirstReference(data, v.issuer, v.fingerprint, installation, operation)
}

// ValidateKeyring checks service configuration before any network request: the
// keyring must be a safe local file containing exactly one primary key, and
// that key must match the pinned fingerprint. It does not authenticate the
// issuer or replace per-response signature verification.
func (v *SignedFirstReferenceVerifier) ValidateKeyring() error {
	if v == nil || !token.MatchString(v.issuer) || !issuerFingerprint.MatchString(v.fingerprint) || !filepath.IsAbs(v.keyring) || filepath.Clean(v.keyring) != v.keyring {
		return RecoveryRequired
	}
	info, err := os.Lstat(v.keyring)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0022 != 0 || info.Size() <= 0 || info.Size() > 4*1024*1024 {
		return RecoveryRequired
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return RecoveryRequired
	}
	before := *stat
	tmp, err := os.MkdirTemp("", "lyra-parental-keyring-check-")
	if err != nil {
		return RecoveryRequired
	}
	defer os.RemoveAll(tmp)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gpg", "--batch", "--no-options", "--no-tty", "--homedir", tmp, "--with-colons", "--show-keys", "--", v.keyring)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "HOME=" + tmp, "GNUPGHOME=" + tmp}
	var output boundedBuffer
	output.limit = 64 * 1024
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err = cmd.Run(); err != nil || ctx.Err() != nil {
		return RecoveryRequired
	}
	primaryCount, matchingCount := 0, 0
	waitingFingerprint := false
	for _, line := range strings.Split(output.String(), "\n") {
		fields := strings.Split(line, ":")
		switch {
		case len(fields) > 0 && fields[0] == "pub":
			primaryCount++
			waitingFingerprint = true
		case waitingFingerprint && len(fields) > 9 && fields[0] == "fpr":
			waitingFingerprint = false
			fingerprint := strings.ToUpper(fields[9])
			if issuerFingerprint.MatchString(fingerprint) && fingerprint == v.fingerprint {
				matchingCount++
			}
		}
	}
	if primaryCount != 1 || matchingCount != 1 {
		return RecoveryRequired
	}
	afterInfo, err := os.Lstat(v.keyring)
	if err != nil || !afterInfo.Mode().IsRegular() || afterInfo.Mode()&0022 != 0 || afterInfo.Size() != info.Size() {
		return RecoveryRequired
	}
	after, ok := afterInfo.Sys().(*syscall.Stat_t)
	if !ok || before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim || after.Uid != uint32(os.Geteuid()) || after.Nlink != 1 {
		return RecoveryRequired
	}
	return nil
}

func decodeSignedFirstReference(data []byte, issuer, fingerprint, installation, operation string) (SignedFirstReference, error) {
	invalid := SignedFirstReference{}
	if len(data) == 0 || len(data) > SignedIssuerEnvelopeLimit || !utf8.Valid(data) {
		return invalid, InvalidReference
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	raw, err := readJSON(d, 0)
	if err != nil {
		return invalid, InvalidReference
	}
	if _, err = d.Token(); err != io.EOF {
		return invalid, InvalidReference
	}
	row, ok := object(raw, "schema", "issuer", "key_fingerprint", "installation", "operation", "reference")
	if !ok {
		return invalid, InvalidReference
	}
	if schema, valid := positive(row["schema"], 1); !valid || schema != 1 {
		return invalid, InvalidReference
	}
	gotIssuer, valid := matching(row["issuer"], token)
	if !valid || gotIssuer != issuer {
		return invalid, Denied
	}
	gotFingerprint, valid := matching(row["key_fingerprint"], issuerFingerprint)
	if !valid || gotFingerprint != fingerprint {
		return invalid, Denied
	}
	gotInstallation, valid := matching(row["installation"], token)
	if !valid || gotInstallation != installation {
		return invalid, Denied
	}
	gotOperation, valid := matching(row["operation"], token)
	if !valid || gotOperation != operation {
		return invalid, Denied
	}
	refData, err := json.Marshal(row["reference"])
	if err != nil {
		return invalid, InvalidReference
	}
	reference, err := DecodeRecoveryReference(refData)
	if err != nil || reference.Installation() != installation || reference.Mode() != "first-install" || reference.Sequence() != 1 || reference.Catalog().Revision() != 1 {
		return invalid, InvalidReference
	}
	return SignedFirstReference{gotIssuer, gotFingerprint, gotOperation, reference}, nil
}

func verifyIssuerSignature(keyring, fingerprint string, data, signature []byte, timeout time.Duration) error {
	info, err := os.Lstat(keyring)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0022 != 0 {
		return RecoveryRequired
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return RecoveryRequired
	}
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 5 * time.Second
	}
	tmp, err := os.MkdirTemp("", "lyra-parental-issuer-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	dataPath := filepath.Join(tmp, "reference.json")
	signaturePath := filepath.Join(tmp, "reference.json.asc")
	if err = os.WriteFile(dataPath, data, 0600); err != nil {
		return err
	}
	if err = os.WriteFile(signaturePath, signature, 0600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gpgv", "--status-fd=1", "--keyring", keyring, "--", signaturePath, dataPath)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "HOME=" + tmp, "GNUPGHOME=" + tmp}
	var status boundedBuffer
	status.limit = 64 * 1024
	cmd.Stdout = &status
	cmd.Stderr = io.Discard
	if err = cmd.Run(); err != nil || ctx.Err() != nil {
		return Denied
	}
	valid := 0
	for _, line := range strings.Split(status.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "[GNUPG:]" {
			continue
		}
		if fields[1] != "VALIDSIG" {
			if fields[1] == "BADSIG" || fields[1] == "ERRSIG" || fields[1] == "NO_PUBKEY" {
				return Denied
			}
			continue
		}
		valid++
		primary := fields[2]
		if len(fields) > 11 && fields[11] != "" {
			primary = fields[11]
		}
		if !issuerFingerprint.MatchString(primary) || primary != fingerprint {
			return Denied
		}
	}
	if valid != 1 {
		return Denied
	}
	return nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, io.ErrShortWrite
	}
	return b.Buffer.Write(p)
}
