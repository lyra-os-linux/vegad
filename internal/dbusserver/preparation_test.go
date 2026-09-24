package dbusserver

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/distro"
	"github.com/lyraos/vegad/internal/profile"
)

func TestPreparationStatusPersistenceAndClassification(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		err         error
		state, kind string
	}{
		{"network", &distro.RepositoryRefreshError{Kind: "network", Err: errors.New("https://user:secret@example.invalid")}, "failed", "network"},
		{"key", &distro.UntrustedKeyError{KeyId: "secret"}, "awaiting-approval", "untrusted-key"},
		{"other", errors.New("private diagnostic secret"), "failed", "operation-failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := preparationFailure("refreshing", tc.err, now)
			if status.State != tc.state || status.ErrorKind != tc.kind || strings.Contains(status.LastError, "secret") {
				t.Fatalf("status: %+v", status)
			}
			if status.NextRetryAt != "2026-09-24T12:15:00Z" {
				t.Fatalf("retry estimate: %s", status.NextRetryAt)
			}
			path := filepath.Join(t.TempDir(), "state.json")
			if err := persistPreparationStatus(path, status); err != nil {
				t.Fatal(err)
			}
			// A new reader reconstructs the state without any in-memory daemon data.
			got, err := readPreparationStatus(path)
			if err != nil || got != status {
				t.Fatalf("persisted: %+v, %v", got, err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0644 {
				t.Fatalf("public summary permissions: %v, %v", info, err)
			}
		})
	}
}

func TestPreparationStatusReconcilesSystemd(t *testing.T) {
	for _, tc := range []struct {
		name, state, active, sub, load, want string
		retry, next                          bool
	}{
		{"pending", "pending", "inactive", "dead", "loaded", "pending", true, false},
		{"running", "failed", "active", "running", "loaded", "running", false, false},
		{"retry", "failed", "activating", "auto-restart", "loaded", "waiting-retry", true, true},
		{"key needs action", "awaiting-approval", "activating", "auto-restart", "loaded", "awaiting-approval", true, true},
		{"limit reached", "waiting-retry", "failed", "failed", "loaded", "failed", true, false},
		{"interrupted", "running", "inactive", "dead", "loaded", "failed", true, false},
		{"completed", "completed", "inactive", "dead", "loaded", "completed", false, false},
		{"legacy", "skipped", "inactive", "dead", "loaded", "skipped", false, false},
		{"missing service", "pending", "inactive", "dead", "not-found", "unavailable", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := reconcilePreparationStatus(PreparationStatus{State: tc.state, NextRetryAt: "estimated"}, preparationUnit{Active: tc.active, Sub: tc.sub, Load: tc.load})
			if got.State != tc.want || got.CanRetry != tc.retry || (got.NextRetryAt != "") != tc.next {
				t.Fatalf("status: %+v", got)
			}
			if tc.name == "interrupted" && got.ErrorKind != "interrupted" {
				t.Fatalf("interruption lost: %+v", got)
			}
		})
	}
}

func TestPreparationRetryUsesSafeFixedCommands(t *testing.T) {
	var calls [][]string
	err := retryPreparation(true, func(args ...string) error { calls = append(calls, args); return nil })
	want := [][]string{{"reset-failed", firstUpdateUnit}, {"--no-block", "start", firstUpdateUnit}}
	if err != nil || !reflect.DeepEqual(calls, want) {
		t.Fatalf("commands: %v, %v", calls, err)
	}
	calls = nil
	if err := retryPreparation(false, func(args ...string) error { calls = append(calls, args); return nil }); err != nil || !reflect.DeepEqual(calls, want[1:]) {
		t.Fatalf("inactive unit retry: %v, %v", calls, err)
	}
	failure := errors.New("systemd unavailable")
	count := 0
	err = retryPreparation(true, func(...string) error { count++; return failure })
	if !errors.Is(err, failure) || count != 1 {
		t.Fatalf("failure handling: %v, %d", err, count)
	}
}

func TestPreparationServiceAuthorizationAndContract(t *testing.T) {
	service := &PreparationService{activity: &Activity{}, profile: profile.Server}
	if err := service.Retry(""); err == nil || err.Name != BusName+".Error.AuthorizationFailed" {
		t.Fatalf("retry accepted without sender: %v", err)
	}
	status, err := service.GetStatus()
	if err != nil || status.State != "unavailable" || status.CanRetry {
		t.Fatalf("server status: %+v, %v", status, err)
	}
	if got := dbus.SignatureOf(PreparationStatus{}).String(); got != "(ssssssb)" {
		t.Fatalf("wire signature: %s", got)
	}
}

func TestPreparationPublishesPhaseAndFailure(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "first-update.done")
	t.Setenv("VEGAD_UPDATE_STATE", filepath.Join(t.TempDir(), "updates.json"))
	var calls []string
	backend := preparationBackend{calls: &calls, fail: "refresh"}
	importKeys := func() error {
		state, err := readPreparationStatus(preparationStatePath(marker))
		if err != nil || state.State != "running" || state.Phase != "importing-keys" {
			t.Fatalf("initial phase: %+v, %v", state, err)
		}
		return nil
	}
	if err := prepareInitialRepositories(profile.Desktop, marker, backend, importKeys, func(UpdateStatus) error { return nil }); err == nil {
		t.Fatal("expected failure")
	}
	state, err := readPreparationStatus(preparationStatePath(marker))
	if err != nil || state.State != "failed" || state.Phase != "refreshing" {
		t.Fatalf("failure phase: %+v, %v", state, err)
	}
	backend.fail = ""
	if err := prepareInitialRepositories(profile.Desktop, marker, backend, importKeys, func(UpdateStatus) error { return nil }); err != nil {
		t.Fatal(err)
	}
	state, err = readPreparationStatus(preparationStatePath(marker))
	if err != nil || state.State != "completed" || state.LastError != "" {
		t.Fatalf("completion: %+v, %v", state, err)
	}
}
