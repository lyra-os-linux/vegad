package distro

import (
	"errors"
	"testing"
)

func TestRepositoryRefreshFailureClassification(t *testing.T) {
	cause := errors.New("exit failure")
	for _, tc := range []struct{ output, kind string }{
		{"Download (curl) error: Could not resolve host: example.invalid", "network"},
		{"Failed to connect to example.invalid port 443", "network"},
		{"Signature verification failed", "refresh"},
		{"System management is locked", "refresh"},
	} {
		err := repositoryRefreshError(tc.output, cause)
		var refresh *RepositoryRefreshError
		if !errors.As(err, &refresh) || refresh.Kind != tc.kind || !errors.Is(err, cause) {
			t.Fatalf("%q: %v", tc.output, err)
		}
	}
	err := repositoryRefreshError("Key Fingerprint: 1C59 D66F CD52 563A 1693 3DBC FEC2 8EAF 09D9 EA69\nKey Name: test\n", cause)
	var key *UntrustedKeyError
	if !errors.As(err, &key) {
		t.Fatalf("untrusted key not distinguished: %v", err)
	}
}
