package dbusserver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lyraos/vegad/internal/profile"
)

// withFlatpakInstalled makes flatpakEnabled see an installed Flatpak; CI
// runners do not ship /usr/bin/flatpak.
func withFlatpakInstalled(t *testing.T) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "flatpak")
	if err := os.WriteFile(binary, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	previous := flatpakBinary
	flatpakBinary = binary
	t.Cleanup(func() { flatpakBinary = previous })
}

func TestFlatpakCapabilityFollowsProfile(t *testing.T) {
	withFlatpakInstalled(t)

	if !(&SoftwareService{profile: profile.Desktop}).flatpakEnabled() {
		t.Fatal("desktop profile must enable Flatpak")
	}
	if (&SoftwareService{profile: profile.Server}).flatpakEnabled() {
		t.Fatal("server profile must disable Flatpak")
	}
}

func TestCapabilityUnavailableUsesStableDBusError(t *testing.T) {
	err := capabilityUnavailable("flatpak")
	if err.Name != BusName+".Error.CapabilityUnavailable" {
		t.Fatalf("error name = %q", err.Name)
	}
}

func TestFlatpakIsOptionalOnTheDesktop(t *testing.T) {
	previous := flatpakBinary
	flatpakBinary = filepath.Join(t.TempDir(), "missing-flatpak")
	t.Cleanup(func() { flatpakBinary = previous })

	if (&SoftwareService{profile: profile.Desktop}).flatpakEnabled() {
		t.Fatal("desktop without the flatpak binary must not query Flatpak")
	}
}
