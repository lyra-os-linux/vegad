package dbusserver

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lyraos/vegad/internal/profile"
)

func TestTrustedKeyMaintenanceIndependentOfPreparation(t *testing.T) {
	for _, state := range []string{"pending", "done", "skipped"} {
		t.Run(state, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "first-update.done")
			t.Setenv("VEGAD_FIRST_UPDATE_MARKER", marker)
			path := marker
			if state == "skipped" {
				path = firstUpdateSkipPath()
			}
			if state != "pending" {
				if err := os.WriteFile(path, []byte("original\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			importKeys := func() error { calls++; return nil }
			for i := 0; i < 2; i++ {
				if err := maintainTrustedKeys(profile.Desktop, "root=UUID=installed", importKeys); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 2 {
				t.Fatalf("maintenance suppressed by preparation state: %d", calls)
			}
			if state != "pending" {
				if data, err := os.ReadFile(path); err != nil || string(data) != "original\n" {
					t.Fatalf("preparation state changed: %q, %v", data, err)
				}
			} else if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("maintenance marked preparation complete: %v", err)
			}
		})
	}
}

func TestTrustedKeyMaintenanceGuardsAndRetry(t *testing.T) {
	for _, tc := range []struct {
		profile profile.Profile
		cmdline string
	}{
		{profile.Server, "root=UUID=installed"},
		{profile.Desktop, "rd.live.image"},
		{profile.Desktop, "root=live:CDLABEL=Lyra"},
	} {
		if err := maintainTrustedKeys(tc.profile, tc.cmdline, func() error { t.Fatal("unexpected key import"); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	failure := errors.New("RPM database locked")
	if err := maintainTrustedKeys(profile.Desktop, "", func() error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("failure lost: %v", err)
	}
	if err := maintainTrustedKeys(profile.Desktop, "", func() error { return nil }); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
}
