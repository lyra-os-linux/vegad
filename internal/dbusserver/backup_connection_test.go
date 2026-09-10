package dbusserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackupConnectionOnceAcrossRestartFailureAndRemount(t *testing.T) {
	state := filepath.Join(t.TempDir(), "last")
	calls := 0
	failed := errors.New("backup failed")
	for i := 0; i < 20; i++ {
		err := backupConnectionOnce(state, "boot-a:mount-10", func() error { calls++; return failed })
		if i == 0 && !errors.Is(err, failed) {
			t.Fatalf("failure not reported: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("persistent connection retried %d times", calls)
	}
	for _, identity := range []string{"boot-a:mount-11", "boot-b:mount-11"} {
		if err := backupConnectionOnce(state, identity, func() error { calls++; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("remount/reboot missed: %d calls", calls)
	}
}

func TestBackupConnectionClaimExcludesConcurrentServices(t *testing.T) {
	state := filepath.Join(t.TempDir(), "last")
	var calls atomic.Int32
	started := make(chan struct{})
	finish := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		if err := backupConnectionOnce(state, "mount", func() error {
			calls.Add(1)
			close(started)
			<-finish
			return nil
		}); err != nil {
			t.Error(err)
		}
	}()
	<-started
	if err := backupConnectionOnce(state, "mount", func() error { calls.Add(1); return nil }); err != nil {
		t.Fatal(err)
	}
	close(finish)
	workers.Wait()
	if calls.Load() != 1 {
		t.Fatal("concurrent service duplicated backup")
	}
}

func TestBackupConnectionWatcherWaitsForMountAndDoesNotFinishWhileConnected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	state := filepath.Join(t.TempDir(), "last")
	checks, backups, reports := 0, 0, 0
	err := watchBackupConnection(ctx, time.Millisecond,
		func() bool { return checks < 12 },
		func() error {
			checks++
			if checks < 4 {
				return errBackupDeferred // Device present but not mounted yet.
			}
			mount := "mount-1"
			if checks >= 9 {
				mount = "mount-2" // Reconnected before the watcher saw absence.
			}
			return backupConnectionOnce(state, mount, func() error {
				backups++
				return errors.New("injected restic failure")
			})
		}, func(error) { reports++ })
	if err != nil || checks != 12 || backups != 2 || reports != 2 {
		t.Fatalf("checks=%d backups=%d reports=%d err=%v", checks, backups, reports, err)
	}
}

func TestBackupConnectionWatcherStopsPromptlyOnServiceStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	err := watchBackupConnection(ctx, time.Hour, func() bool { return true }, func() error {
		cancel()
		return errBackupDeferred
	}, func(error) { t.Fatal("unexpected error") })
	if err != nil {
		t.Fatal(err)
	}
}

func mountedBackupFixture(t *testing.T) BackupConfig {
	t.Helper()
	if _, err := exec.LookPath("findmnt"); err != nil {
		t.Skip("findmnt unavailable")
	}
	base := t.TempDir()
	t.Setenv("VEGA_BACKUP_STATE_DIR", filepath.Join(base, "state"))
	mount, err := findBackupMount("--target", base)
	if err != nil {
		t.Fatal(err)
	}
	cfg := BackupConfig{Id: "mounted", Destination: filepath.Join(base, "repo"), Paths: []string{filepath.Join(base, "source")}, Frequency: "on-connect"}
	if err := os.MkdirAll(backupConnectionDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(mount)
	if err := os.WriteFile(backupMountTargetPath(cfg.Id), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestMountedBackupAnchorsRepositoryAndRejectsSymlinks(t *testing.T) {
	cfg := mountedBackupFixture(t)
	err := withMountedBackup(cfg, func(resolved BackupConfig, _ backupMount) error {
		if !strings.HasPrefix(resolved.Destination, "/proc/") || resolved.DestinationUUID != "" || resolved.Frequency != "manual" {
			t.Fatalf("unanchored destination: %+v", resolved)
		}
		return os.WriteFile(filepath.Join(resolved.Destination, "marker"), []byte("mounted volume"), 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRestoreFile(t, cfg.Destination, "marker", "mounted volume")
	outside := t.TempDir()
	link := filepath.Join(filepath.Dir(cfg.Destination), "symlink")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	cfg.Destination = filepath.Join(link, "must-not-create")
	if err := withMountedBackup(cfg, func(BackupConfig, backupMount) error {
		t.Fatal("followed destination symlink")
		return nil
	}); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "must-not-create")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote outside mounted destination")
	}
}

func TestMountedBackupRejectsFallbackFilesystemAndStaleMountID(t *testing.T) {
	cfg := mountedBackupFixture(t)
	data, err := os.ReadFile(backupMountTargetPath(cfg.Id))
	if err != nil {
		t.Fatal(err)
	}
	var expected backupMount
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	expected.ID++
	if file, err := openBackupMount(expected); err == nil {
		file.Close()
		t.Fatal("stale mount ID accepted")
	}
	expected.ID--
	expected.UniqueID++
	if file, err := openBackupMount(expected); err == nil {
		file.Close()
		t.Fatal("reused mount ID accepted for a different unique mount")
	}
	expected.Source = "absent-device"
	data, _ = json.Marshal(expected)
	if err := os.WriteFile(backupMountTargetPath(cfg.Id), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := withMountedBackup(cfg, func(BackupConfig, backupMount) error {
		t.Fatal("backup ran on fallback filesystem")
		return nil
	}); !errors.Is(err, errBackupDeferred) {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(cfg.Destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unavailable destination created on host filesystem")
	}
}

func TestConnectedBackupRunsRealResticOnlyOnceAndAllowsManualRetry(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic unavailable")
	}
	cfg := mountedBackupFixture(t)
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "no-bus"))
	t.Setenv("RESTIC_CACHE_DIR", t.TempDir())
	if err := ensureBackupDirs(); err != nil {
		t.Fatal(err)
	}
	if err := ensureBackupPassword(cfg.Id); err != nil {
		t.Fatal(err)
	}
	if err := writeBackupConfig(backupConfigPath(cfg.Id), cfg); err != nil {
		t.Fatal(err)
	}
	restoreFixture(t, cfg.Paths[0], "source.txt", "real backup payload")
	report := func(uint32, string) {}
	for i := 0; i < 3; i++ {
		if err := runConnectedBackup(cfg, report); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := resticSnapshots(cfg)
	if err != nil || len(rows) != 1 {
		t.Fatalf("persistent connection: snapshots=%d error=%v", len(rows), err)
	}
	if err := RunBackupJob(cfg.Id, report); err != nil {
		t.Fatal(err)
	}
	rows, err = resticSnapshots(cfg)
	if err != nil || len(rows) != 2 {
		t.Fatalf("manual retry: snapshots=%d error=%v", len(rows), err)
	}
}

func TestOnConnectCreationWaitsForMountedVolumeBeforeCreatingRepository(t *testing.T) {
	cfg := mountedBackupFixture(t)
	unitDir := t.TempDir()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin) // No restic, no host systemd or session keyring.
	if err := createBackupConfig(cfg, unitDir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.Destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("creation initialized repository before the mounted job")
	}
	trigger := backupPathTrigger(cfg)
	if trigger == cfg.Destination || !backupDestinationPathAvailable(trigger) {
		t.Fatalf("new repository can never activate its path: %q", trigger)
	}
	service, _ := os.ReadFile(filepath.Join(unitDir, backupServiceUnitName(cfg.Id)))
	if !strings.Contains(string(service), "Type=simple\n") {
		t.Fatal("watcher is not a long-running service")
	}
}

func TestBackupConfigRejectsInvalidConnectionPaths(t *testing.T) {
	for _, destination := range []string{"relative/repo", "/tmp/repo\nUnit=evil.service", "/tmp/repo\x00", "https://example.org/repo"} {
		if _, err := normalizeBackupConfig(BackupConfig{Id: "invalid", Paths: []string{"/home"}, Destination: destination, Frequency: "on-connect"}); err == nil {
			t.Fatalf("accepted destination %q", destination)
		}
	}
	for _, destination := range []string{"../escape", "repo/../../escape"} {
		if _, err := normalizeBackupConfig(BackupConfig{Id: "invalid", Paths: []string{"/home"}, Destination: destination, DestinationUUID: "test-uuid", Frequency: "on-connect"}); err == nil {
			t.Fatalf("accepted relative UUID destination %q", destination)
		}
	}
}

func TestBackupRemovalStopsWatcherBeforeRemovingUnitsAndRetainsArtifactsOnFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			bin, units := t.TempDir(), t.TempDir()
			logPath := filepath.Join(bin, "calls")
			t.Setenv("VEGA_TEST_SYSTEMCTL_LOG", logPath)
			t.Setenv("VEGA_TEST_STOP_FAILURE", fmt.Sprint(fail))
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$VEGA_TEST_SYSTEMCTL_LOG\"\n" +
				"case \"$1\" in show) printf 'loaded\\n';; stop) [ \"$VEGA_TEST_STOP_FAILURE\" != true ];; esac\n"
			if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			service := backupServiceUnitName("example")
			restoreFixture(t, units, service, "watcher")
			err := removeBackupSystemdUnitsAt("example", units)
			if (err != nil) != fail {
				t.Fatalf("stop failure=%t: %v", fail, err)
			}
			log, _ := os.ReadFile(logPath)
			if !strings.Contains(string(log), "stop "+service+"\n") {
				t.Fatalf("watcher not stopped: %s", log)
			}
			if fail {
				assertRestoreFile(t, units, service, "watcher")
			} else if _, err := os.Stat(filepath.Join(units, service)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("stopped unit not removed")
			}
		})
	}
}
