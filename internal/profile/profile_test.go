package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Setenv("VEGAD_PROFILE", "server")
	p, source, err := Load("/does/not/matter")
	if err != nil || p != Server || source != "VEGAD_PROFILE" {
		t.Fatalf("Load env = %q, %q, %v", p, source, err)
	}

	os.Unsetenv("VEGAD_PROFILE")
	path := filepath.Join(t.TempDir(), "vegad.conf")
	if err := os.WriteFile(path, []byte("# Vega\nVEGAD_PROFILE=desktop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, source, err = Load(path)
	if err != nil || p != Desktop || source != path {
		t.Fatalf("Load file = %q, %q, %v", p, source, err)
	}
}

func useServerMarker(t *testing.T, path string) {
	t.Helper()
	previous := serverMarkerPath
	serverMarkerPath = path
	t.Cleanup(func() { serverMarkerPath = previous })
}

func TestLoadDefaultsToDesktop(t *testing.T) {
	t.Setenv("VEGAD_PROFILE", "")
	os.Unsetenv("VEGAD_PROFILE")
	useServerMarker(t, filepath.Join(t.TempDir(), "no-server-release"))
	p, source, err := Load(filepath.Join(t.TempDir(), "missing"))
	if err != nil || p != Desktop || source != "compatibility default" {
		t.Fatalf("Load default = %q, %q, %v", p, source, err)
	}
}

func TestLoadDetectsServerEdition(t *testing.T) {
	t.Setenv("VEGAD_PROFILE", "")
	os.Unsetenv("VEGAD_PROFILE")
	marker := filepath.Join(t.TempDir(), "server-release")
	if err := os.WriteFile(marker, []byte("LYRA_ARTIFACT_VERSION='16.1-rc.1'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	useServerMarker(t, marker)

	// No configuration file: the Server marker selects the server profile.
	p, source, err := Load(filepath.Join(t.TempDir(), "missing"))
	if err != nil || p != Server || source != "edition marker "+marker {
		t.Fatalf("Load marker = %q, %q, %v", p, source, err)
	}

	// The packaged template only has comments: still detected.
	path := filepath.Join(t.TempDir(), "vegad.conf")
	if err := os.WriteFile(path, []byte("# Perfil do vegad\n#VEGAD_PROFILE=desktop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, source, err = Load(path)
	if err != nil || p != Server || source != "edition marker "+marker {
		t.Fatalf("Load template = %q, %q, %v", p, source, err)
	}

	// An explicit value always wins over the marker.
	if err := os.WriteFile(path, []byte("VEGAD_PROFILE=desktop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, source, err = Load(path)
	if err != nil || p != Desktop || source != path {
		t.Fatalf("Load explicit = %q, %q, %v", p, source, err)
	}
}

func TestInvalidProfileFails(t *testing.T) {
	t.Setenv("VEGAD_PROFILE", "workstation")
	if _, _, err := Load("ignored"); err == nil {
		t.Fatal("expected invalid profile error")
	}
}

func TestCapabilities(t *testing.T) {
	if !Desktop.Has("flatpak") || Server.Has("flatpak") || !Server.Has("packages-native") {
		t.Fatal("profile capability matrix is inconsistent")
	}
}

func TestCapabilitiesHaveNoNvidia(t *testing.T) {
	for _, p := range []Profile{Desktop, Server} {
		for _, capability := range p.Capabilities() {
			if strings.HasPrefix(capability, "nvidia-") {
				t.Fatalf("profile %s still announces %s", p, capability)
			}
		}
	}
}
