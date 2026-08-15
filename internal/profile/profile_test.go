package profile

import (
	"os"
	"path/filepath"
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

func TestLoadDefaultsToDesktop(t *testing.T) {
	t.Setenv("VEGAD_PROFILE", "")
	os.Unsetenv("VEGAD_PROFILE")
	p, source, err := Load(filepath.Join(t.TempDir(), "missing"))
	if err != nil || p != Desktop || source != "compatibility default" {
		t.Fatalf("Load default = %q, %q, %v", p, source, err)
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
