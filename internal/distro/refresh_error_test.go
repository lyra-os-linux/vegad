package distro

import (
	"errors"
	"strings"
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

// The Kind preparation relies on survives the user-facing explanation.
func TestRepositoryRefreshFailureIsExplained(t *testing.T) {
	previous := zypperWarnf
	zypperWarnf = func(string, ...any) {}
	defer func() { zypperWarnf = previous }()
	cause := errors.New("exit failure")
	err := repositoryRefreshError("Repository 'microsoft-prod' is invalid.\nDownload (curl) error for 'https://packages.microsoft.com/opensuse/16/prod/repodata/repomd.xml':\nError message: Could not resolve host: packages.microsoft.com", cause)
	var refresh *RepositoryRefreshError
	var friendly *ZypperError
	if !errors.As(err, &refresh) || refresh.Kind != "network" || !errors.As(err, &friendly) || friendly.Kind != "repository" || !errors.Is(err, cause) {
		t.Fatalf("error = %#v", err)
	}
	if want := "Não foi possível acessar o repositório microsoft-prod"; !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("message = %q, want prefix %q", err.Error(), want)
	}
}
