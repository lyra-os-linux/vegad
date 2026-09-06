package dbusserver

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func backupCreateFixture(t *testing.T) (BackupConfig, string) {
	t.Helper()
	restic, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("restic unavailable")
	}
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(restic, filepath.Join(bin, "restic")); err != nil {
		t.Fatal(err)
	}
	// No host systemd calls or session keyring access.
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\nif [ \"$1\" = enable ] && [ \"${VEGA_TEST_ACTIVATION_FAILURE:-}\" = 1 ]; then exit 1; fi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("VEGA_BACKUP_STATE_DIR", filepath.Join(base, "state"))
	t.Setenv("RESTIC_CACHE_DIR", filepath.Join(base, "cache"))
	return BackupConfig{Id: "recoverable", Paths: []string{filepath.Join(base, "source")}, Destination: filepath.Join(base, "repo"), Frequency: "daily"}, filepath.Join(base, "units")
}

func TestBackupCreationResumesEveryStepWithSameRepositoryAndPassword(t *testing.T) {
	for _, phase := range []string{"pending", "password", "repository", "units", "config", "activation"} {
		t.Run(phase, func(t *testing.T) {
			cfg, unitDir := backupCreateFixture(t)
			err := createBackupConfig(cfg, unitDir, func(step string) error {
				if step == phase {
					return syscall.ENOSPC
				}
				return nil
			})
			if !errors.Is(err, syscall.ENOSPC) {
				t.Fatalf("injected failure lost: %v", err)
			}
			if _, err := os.Stat(backupPendingPath(cfg.Id)); err != nil {
				t.Fatalf("no recovery record: %v", err)
			}
			passwordBefore, _ := os.ReadFile(backupPasswordPath(cfg.Id))
			repoBefore, _ := os.ReadFile(filepath.Join(cfg.Destination, "config"))
			if err := createBackupConfig(cfg, unitDir, nil); err != nil {
				t.Fatalf("same-ID retry failed: %v", err)
			}
			passwordAfter, err := os.ReadFile(backupPasswordPath(cfg.Id))
			if err != nil || len(passwordAfter) == 0 || (len(passwordBefore) > 0 && string(passwordBefore) != string(passwordAfter)) {
				t.Fatal("credential changed or unavailable after retry")
			}
			repoAfter, err := os.ReadFile(filepath.Join(cfg.Destination, "config"))
			if err != nil || (len(repoBefore) > 0 && string(repoBefore) != string(repoAfter)) {
				t.Fatal("repository replaced or unavailable after retry")
			}
			if err := ensureResticRepository(cfg, nil); err != nil {
				t.Fatalf("repository no longer accessible: %v", err)
			}
			if _, err := os.Stat(backupPendingPath(cfg.Id)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("completed operation still pending: %v", err)
			}
			for _, path := range []string{backupConfigPath(cfg.Id), backupPasswordPath(cfg.Id)} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("private file permissions: %s, %v", path, err)
				}
			}
			if err := createBackupConfig(cfg, unitDir, nil); err == nil {
				t.Fatal("completed config accepted as a fresh creation")
			}
		})
	}
}

func TestBackupCreationActivationFailureRemainsRecoverable(t *testing.T) {
	cfg, unitDir := backupCreateFixture(t)
	t.Setenv("VEGA_TEST_ACTIVATION_FAILURE", "1")
	if err := createBackupConfig(cfg, unitDir, nil); err == nil || !strings.Contains(err.Error(), "activation") {
		t.Fatalf("activation failure: %v", err)
	}
	if _, err := readBackupConfig(cfg.Id); err != nil {
		t.Fatalf("prepared configuration lost: %v", err)
	}
	t.Setenv("VEGA_TEST_ACTIVATION_FAILURE", "")
	if err := createBackupConfig(cfg, unitDir, nil); err != nil {
		t.Fatalf("retry after systemd failure: %v", err)
	}
}

func TestBackupCreationDoesNotOverwriteUnrelatedArtifacts(t *testing.T) {
	cfg, unitDir := backupCreateFixture(t)
	restoreFixture(t, unitDir, backupServiceUnitName(cfg.Id), "administrator unit")
	if err := createBackupConfig(cfg, unitDir, nil); err == nil {
		t.Fatal("preexisting unit overwritten")
	}
	assertRestoreFile(t, unitDir, backupServiceUnitName(cfg.Id), "administrator unit")
	if _, err := os.Stat(backupPasswordPath(cfg.Id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preflight failure created a credential")
	}
}

func TestBackupCreationRejectsChangedRequestAndHidesUnpreparedConfig(t *testing.T) {
	cfg, unitDir := backupCreateFixture(t)
	if err := createBackupConfig(cfg, unitDir, func(step string) error { return syscall.ENOSPC }); err == nil {
		t.Fatal("expected injected failure")
	}
	rows, err := readBackupConfigs()
	if err != nil || len(rows) != 0 {
		t.Fatalf("unprepared config visible: %v, %v", rows, err)
	}
	cfg.Destination += "-other"
	if err := createBackupConfig(cfg, unitDir, nil); err == nil {
		t.Fatal("different parameters accepted for pending ID")
	}
}
