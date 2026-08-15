package dbusserver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRestoreTargetRejectsDangerousPaths(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home", "alice")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	caller := &desktopUser{Uid: 1000, Username: "alice", HomeDir: home}

	for _, target := range []string{"", ".", "relative/path", "/", "/etc", "/etc/ssh", "/usr", "/var", "/home", filepath.Join(home, "..", "bob")} {
		t.Run(target, func(t *testing.T) {
			if _, err := validateRestoreTarget(target, caller, nil); err == nil {
				t.Fatalf("dangerous target %q was accepted", target)
			}
		})
	}
}

func TestValidateRestoreTargetRejectsOtherUser(t *testing.T) {
	base := t.TempDir()
	alice := filepath.Join(base, "home", "alice")
	bob := filepath.Join(base, "home", "bob")
	if err := os.MkdirAll(bob, 0o755); err != nil {
		t.Fatal(err)
	}
	caller := &desktopUser{Uid: 1000, Username: "alice", HomeDir: alice}
	if _, err := validateRestoreTarget(bob, caller, nil); err == nil {
		t.Fatal("another user's directory was accepted")
	}
}

func TestValidateRestoreTargetRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home", "alice")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "escape")); err != nil {
		t.Fatal(err)
	}
	caller := &desktopUser{Uid: 1000, Username: "alice", HomeDir: home}
	if _, err := validateRestoreTarget(filepath.Join(home, "escape", "restore"), caller, nil); err == nil {
		t.Fatal("symlink escape was accepted")
	}
}

func TestValidateRestoreTargetAcceptsAllowedDestinations(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home", "alice")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	caller := &desktopUser{Uid: 1000, Username: "alice", HomeDir: home}
	for _, target := range []string{home, filepath.Join(home, "restore"), filepath.Join(home, "missing", "nested")} {
		got, err := validateRestoreTarget(target, caller, nil)
		if err != nil || got != target {
			t.Fatalf("valid target %q = %q, %v", target, got, err)
		}
	}
}

func TestValidateRestoreTargetAllowsRootOnlyInsideBackupRoots(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := validateRestoreTarget(filepath.Join(root, "restore"), nil, []string{root}); err != nil {
		t.Fatalf("root target inside configured backup root rejected: %v", err)
	}
	if _, err := validateRestoreTarget(filepath.Join(t.TempDir(), "other"), nil, []string{root}); err == nil {
		t.Fatal("root target outside configured backup roots accepted")
	}
}

func TestRejectedRestoreDoesNotRemoveAnything(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home", "alice")
	protected := filepath.Join(base, "home", "bob")
	marker := filepath.Join(protected, "keep")
	if err := os.MkdirAll(protected, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	caller := &desktopUser{Uid: 1000, Username: "alice", HomeDir: home}
	if _, err := validateRestoreTarget(protected, caller, nil); err == nil {
		t.Fatal("dangerous restore unexpectedly accepted")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("validation changed the target: %v", err)
	}
}
