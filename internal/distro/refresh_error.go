package distro

import (
	"fmt"
	"strings"
)

// RepositoryRefreshError preserves the command failure while exposing a narrow
// classification for user-facing preparation state. Unknown failures stay generic.
type RepositoryRefreshError struct {
	Kind string
	Err  error
}

func (e *RepositoryRefreshError) Error() string { return e.Err.Error() }
func (e *RepositoryRefreshError) Unwrap() error { return e.Err }

func repositoryRefreshError(output string, cause error) error {
	if key, ok := parseZypperUntrustedKey("", output); ok {
		return key
	}
	kind := "refresh"
	lower := strings.ToLower(output)
	for _, text := range []string{"could not resolve host", "couldn't resolve host", "couldn't connect to server", "failed to connect", "network is unreachable", "connection timed out", "timeout was reached"} {
		if strings.Contains(lower, text) {
			kind = "network"
			break
		}
	}
	return &RepositoryRefreshError{Kind: kind, Err: fmt.Errorf("zypper refresh: %w — %s", cause, output)}
}
