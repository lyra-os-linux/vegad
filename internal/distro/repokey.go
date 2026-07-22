package distro

import (
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// fetchRepoKey downloads keyURL and returns its raw bytes (ASCII-armored or
// binary OpenPGP key). ok=false (not an error) means the URL simply didn't
// have a key — callers (dnfBackend/aptBackend) treat that as "this repo has
// no discoverable signing key", adding it unverified rather than failing.
func fetchRepoKey(keyURL string) (data []byte, ok bool) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(keyURL)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || len(body) == 0 {
		return nil, false
	}
	return body, true
}

// inspectGPGKey shells out to `gpg --with-colons --show-keys` to read the
// fingerprint and primary user ID out of a raw key blob, without importing
// it into any keyring — this is only a preview, used to show the user what
// they'd be trusting before AddRepo/TrustRepoKey actually imports it.
//
// GnuPG's colon output (doc/DETAILS in the gnupg source) uses field 10
// (1-indexed) both for a "uid" record's User-ID string and a "fpr" record's
// fingerprint — verified against a real key: `gpg --with-colons --show-keys`
// on openSUSE:Factory's repomd.xml.key produces
// "fpr:::::::::AD48...B700A4:" and "uid:-::::...::...::openSUSE Project
// Signing Key <opensuse@opensuse.org>::::::::::0:".
func inspectGPGKey(keyData []byte) (fingerprint, userId string, err error) {
	cmd := exec.Command("gpg", "--with-colons", "--show-keys")
	cmd.Stdin = strings.NewReader(string(keyData))
	out, cmdErr := cmd.CombinedOutput()
	if cmdErr != nil {
		return "", "", fmt.Errorf("gpg --show-keys: %w — %s", cmdErr, strings.TrimSpace(string(out)))
	}

	const fieldIdx = 9 // 0-indexed field 10
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) <= fieldIdx {
			continue
		}
		switch fields[0] {
		case "fpr":
			if fingerprint == "" {
				fingerprint = fields[fieldIdx]
			}
		case "uid":
			if userId == "" {
				userId = fields[fieldIdx]
			}
		}
	}
	if fingerprint == "" {
		return "", "", fmt.Errorf("gpg --show-keys: nenhuma fingerprint encontrada na saída")
	}
	return fingerprint, userId, nil
}
