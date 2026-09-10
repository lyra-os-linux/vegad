package dbusserver

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
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

func restoreFixture(t *testing.T, root, path, content string) {
	t.Helper()
	name := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func assertRestoreFile(t *testing.T, root, path, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, path))
	if err != nil || string(got) != want {
		t.Fatalf("%s = %q, %v; want %q", path, got, err, want)
	}
}

func TestStagedRestoreFailurePreservesOriginals(t *testing.T) {
	for _, failure := range []error{syscall.ENOSPC, errors.New("snapshot unavailable"), errors.New("verification failed")} {
		t.Run(failure.Error(), func(t *testing.T) {
			target := t.TempDir()
			restoreFixture(t, target, "selected", "original")
			restoreFixture(t, target, "unselected", "keep")
			err := stagedRestore(target, "replace", "1234", func(stage string) error {
				restoreFixture(t, stage, "selected", "partial data")
				assertRestoreFile(t, target, "selected", "original")
				return failure
			}, func() { t.Error("failed restore reported completion") })
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
			assertRestoreFile(t, target, "selected", "original")
			assertRestoreFile(t, target, "unselected", "keep")
		})
	}
}

func TestStagedRestoreMergesSelectionAndPreservesMetadata(t *testing.T) {
	target := t.TempDir()
	restoreFixture(t, target, "folder/selected", "old")
	restoreFixture(t, target, "folder/unselected", "keep")
	completed := false
	err := stagedRestore(target, "replace", "1234", func(stage string) error {
		restoreFixture(t, stage, "folder/selected", "new")
		restoreFixture(t, stage, "new-folder/file", "created")
		if err := os.Link(filepath.Join(stage, "folder/selected"), filepath.Join(stage, "folder/hardlink")); err != nil {
			t.Fatal(err)
		}
		return os.Symlink("selected", filepath.Join(stage, "folder/link"))
	}, func() { completed = true })
	if err != nil || !completed {
		t.Fatalf("restore = %v, complete = %v", err, completed)
	}
	assertRestoreFile(t, target, "folder/selected", "new")
	assertRestoreFile(t, target, "folder/unselected", "keep")
	assertRestoreFile(t, target, "new-folder/file", "created")
	a, _ := os.Stat(filepath.Join(target, "folder/selected"))
	b, _ := os.Stat(filepath.Join(target, "folder/hardlink"))
	if !os.SameFile(a, b) || a.Mode().Perm() != 0o640 {
		t.Fatal("hardlink identity or file mode was lost")
	}
	if link, err := os.Readlink(filepath.Join(target, "folder/link")); err != nil || link != "selected" {
		t.Fatalf("symlink = %q, %v", link, err)
	}
}

func TestStagedRestoreRejectsDirectoryConflictsBeforeApplying(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		target, outside := t.TempDir(), t.TempDir()
		restoreFixture(t, target, "a-first", "old")
		restoreFixture(t, outside, "keep", "outside")
		if symlink {
			if err := os.Symlink(outside, filepath.Join(target, "z-conflict")); err != nil {
				t.Fatal(err)
			}
		} else {
			restoreFixture(t, target, "z-conflict", "original")
		}
		err := stagedRestore(target, "replace", "1234", func(stage string) error {
			restoreFixture(t, stage, "a-first", "new")
			restoreFixture(t, stage, "z-conflict/keep", "overwrite")
			return nil
		}, func() { t.Error("conflicting restore completed") })
		if err == nil {
			t.Fatal("directory conflict accepted")
		}
		assertRestoreFile(t, target, "a-first", "old")
		assertRestoreFile(t, outside, "keep", "outside")
	}
}

func TestRestoreApplyRollsBackAfterPartialReplacement(t *testing.T) {
	for failAt := 1; failAt <= 4; failAt++ {
		target, stage, originals := t.TempDir(), t.TempDir(), t.TempDir()
		for _, name := range []string{"a", "b"} {
			restoreFixture(t, target, name, "old-"+name)
			restoreFixture(t, stage, name, "new-"+name)
		}
		var moves []restoreMove
		if err := planRestore(stage, target, "", &moves); err != nil {
			t.Fatal(err)
		}
		calls := 0
		err := applyRestoreMoves(stage, target, originals, moves, func(from, to string) error {
			calls++
			if calls == failAt {
				return syscall.ENOSPC
			}
			return os.Rename(from, to)
		})
		if !errors.Is(err, syscall.ENOSPC) {
			t.Fatalf("failure at %d = %v", failAt, err)
		}
		for _, name := range []string{"a", "b"} {
			assertRestoreFile(t, target, name, "old-"+name)
			assertRestoreFile(t, stage, name, "new-"+name)
		}
	}
}

func TestRestoreRollbackFailureRetainsOriginalCopy(t *testing.T) {
	target, stage, originals := t.TempDir(), t.TempDir(), t.TempDir()
	restoreFixture(t, target, "file", "original")
	restoreFixture(t, stage, "file", "new")
	calls := 0
	err := applyRestoreMoves(stage, target, originals, []restoreMove{{Path: "file", HadOriginal: true}}, func(from, to string) error {
		calls++
		if calls > 1 {
			return syscall.EIO
		}
		return os.Rename(from, to)
	})
	if err == nil || !strings.Contains(err.Error(), "recuperar file") {
		t.Fatalf("rollback failure missing: %v", err)
	}
	assertRestoreFile(t, originals, "file", "original")
	assertRestoreFile(t, stage, "file", "new")
}

func TestSeparateRestoreNeverOverwritesEarlierRestore(t *testing.T) {
	target := t.TempDir()
	restoreFixture(t, target, "restored-1234/file", "original")
	err := stagedRestore(target, "separate-folder", "1234", func(stage string) error {
		restoreFixture(t, stage, "file", "new")
		return nil
	}, func() { t.Error("existing separate restore overwritten") })
	if err == nil {
		t.Fatal("expected existing folder error")
	}
	assertRestoreFile(t, target, "restored-1234/file", "original")
}

func TestRestoreBackupUsesVerifiedStagingAndSelection(t *testing.T) {
	bin, target := t.TempDir(), t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("RESTORE_TEST_ARGS", argsFile)
	t.Setenv("PATH", bin)
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$RESTORE_TEST_ARGS\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "restic"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	restoreFixture(t, target, "keep", "original")
	err := restoreBackup(BackupConfig{Destination: "/test/repository"}, "1234", target, "replace", []string{"/selected"}, nil)
	if err == nil {
		t.Fatal("restic failure ignored")
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(args) != 9 || args[5] == target || !reflect.DeepEqual(args[6:], []string{"--verify", "--include", "/selected"}) {
		t.Fatalf("unsafe restic args: %#v", args)
	}
	assertRestoreFile(t, target, "keep", "original")
}

func TestRestoreBackupWithRealRestic(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic is not installed")
	}
	base, target := t.TempDir(), t.TempDir()
	repo := filepath.Join(base, "repo")
	source := filepath.Join(base, "source")
	t.Setenv("RESTIC_PASSWORD", "disposable-fixture-password")
	t.Setenv("RESTIC_CACHE_DIR", filepath.Join(base, "cache"))
	t.Setenv("VEGA_BACKUP_STATE_DIR", filepath.Join(base, "state"))
	restoreFixture(t, source, "selected", "restored")
	restoreFixture(t, source, "unselected", "snapshot version")
	for _, args := range [][]string{{"-r", repo, "init"}, {"-r", repo, "backup", "."}} {
		cmd := exec.Command("restic", args...)
		cmd.Dir = source
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("restic %v: %v\n%s", args, err, out)
		}
	}
	restoreFixture(t, target, "selected", "original")
	restoreFixture(t, target, "unselected", "keep")
	// backupCommandEnv points restic at this disposable config password file.
	if err := os.MkdirAll(backupPasswordsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPasswordPath("test"), []byte("disposable-fixture-password"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := restoreBackup(BackupConfig{Id: "test", Destination: repo}, "latest", target, "replace", []string{"/selected"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertRestoreFile(t, target, "selected", "restored")
	assertRestoreFile(t, target, "unselected", "keep")
}
