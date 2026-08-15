package dbusserver

import (
	"testing"

	"github.com/lyraos/vegad/internal/profile"
)

func TestFlatpakCapabilityFollowsProfile(t *testing.T) {
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
