package dbusserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lyraos/vegad/internal/distro"
	"github.com/lyraos/vegad/internal/profile"
)

func TestPersistUpdateStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "status.json")
	want := UpdateStatus{CheckedAt: "2026-08-15T12:00:00Z", Profile: "desktop", NativeCount: 2, FlatpakCount: 1, TotalCount: 3}
	if err := persistUpdateStatus(path, want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got UpdateStatus
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("status = %#v, want %#v", got, want)
	}
	loaded, err := readUpdateStatus(path)
	if err != nil || loaded != want {
		t.Fatalf("read status = %#v, %v", loaded, err)
	}
}

func TestUpdateStatusChangedIgnoresTimestamp(t *testing.T) {
	a := UpdateStatus{CheckedAt: "old", Profile: "desktop", NativeCount: 1, TotalCount: 1}
	b := a
	b.CheckedAt = "new"
	if updateStatusChanged(a, b) {
		t.Fatal("timestamp-only update must not emit a duplicate alert")
	}
	b.TotalCount = 0
	b.NativeCount = 0
	if !updateStatusChanged(a, b) {
		t.Fatal("transition to zero updates must emit an alert")
	}
	a = b
	b.InProgress = true
	if !updateStatusChanged(a, b) {
		t.Fatal("progress transition must emit a state event")
	}
	a = b
	b.InProgress = false
	if updateResultChanged(a, b) {
		t.Fatal("progress-only transition must not duplicate the availability alert")
	}
}

func TestInitialRepositoryStatus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		profile  profile.Profile
		previous UpdateStatus
		flatpak  uint32
	}{
		{"fresh", profile.Desktop, UpdateStatus{}, 0},
		{"cached flatpak", profile.Desktop, UpdateStatus{Profile: "desktop", FlatpakCount: 4, NativeCount: 99, Error: "old refresh failure", InProgress: true}, 4},
		{"different profile", profile.Desktop, UpdateStatus{Profile: "server", FlatpakCount: 4}, 0},
		{"server", profile.Server, UpdateStatus{Profile: "server", FlatpakCount: 4}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := initialRepositoryStatus(tc.profile, []distro.PackageRef{{Id: "vim"}}, tc.previous)
			if status.NativeCount != 1 || status.FlatpakCount != tc.flatpak || status.TotalCount != 1+tc.flatpak || status.Profile != string(tc.profile) || status.CheckedAt == "" || status.Error != "" || status.InProgress {
				t.Fatalf("unexpected status: %+v", status)
			}
		})
	}
}
