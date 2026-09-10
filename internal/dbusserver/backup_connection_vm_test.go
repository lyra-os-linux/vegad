package dbusserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run only in a disposable VM provisioned with the marker below, systemd as
// PID 1, restic/findmnt/mount and this build at /usr/lib/vega/vegad. The test
// creates system units and mounts, so normal go test never enables it.
func TestBackupConnectionSystemdVM(t *testing.T) {
	if os.Getenv("VEGA_BACKUP_VM_TEST") != "1" {
		t.Skip("requires disposable backup VM")
	}
	if _, err := os.Stat("/run/vega-backup-test-vm"); err != nil || os.Geteuid() != 0 {
		t.Fatal("disposable VM marker/root missing")
	}
	for _, uuid := range []bool{false, true} {
		name := "path"
		if uuid {
			name = "uuid"
		}
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			// Include systemd specifiers and quotes in the real mountpoint.
			mountpoint := filepath.Join(base, `volume %n "test"`)
			t.Setenv("VEGA_BACKUP_STATE_DIR", filepath.Join(base, "state"))
			t.Setenv("RESTIC_CACHE_DIR", filepath.Join(base, "cache"))
			cfg := BackupConfig{Id: "vm-" + name, Paths: []string{filepath.Join(base, "source")}, Destination: filepath.Join(mountpoint, "repo"), Frequency: "on-connect"}
			if err := os.MkdirAll(mountpoint, 0o755); err != nil {
				t.Fatal(err)
			}
			command := func(args ...string) string {
				t.Helper()
				out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
				if err != nil {
					t.Fatalf("%v: %v\n%s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			volume := filepath.Join(base, "volume.ext4")
			image, err := os.Create(volume)
			if err != nil {
				t.Fatal(err)
			}
			err = image.Truncate(128 << 20)
			image.Close()
			if err != nil {
				t.Fatal(err)
			}
			volumeUUID := "361fc4f2-3f15-4dcc-a9b0-e9ba4b2ca24f"
			command("mkfs.ext4", "-q", "-F", "-U", volumeUUID, volume)
			device := command("losetup", "--find", "--show", volume)
			t.Cleanup(func() {
				exec.Command("umount", mountpoint).Run()
				exec.Command("losetup", "--detach", device).Run()
			})
			mount := func() { command("mount", "-t", "ext4", device, mountpoint) }
			unmount := func() { command("umount", mountpoint) }
			mount()
			if uuid {
				cfg.DestinationUUID = volumeUUID
				cfg.Destination = "repo"
				if err := os.MkdirAll("/dev/disk/by-uuid", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(device, backupRemovableUUIDPath(cfg.DestinationUUID)); err != nil {
					t.Fatal(err)
				}
			}
			if err := ensureBackupDirs(); err != nil {
				t.Fatal(err)
			}
			if err := ensureBackupPassword(cfg.Id); err != nil {
				t.Fatal(err)
			}
			if err := prepareBackupMountTarget(cfg); err != nil {
				t.Fatal(err)
			}
			if err := writeBackupConfig(backupConfigPath(cfg.Id), cfg); err != nil {
				t.Fatal(err)
			}
			if err := writeBackupSystemdUnitsAt(cfg, backupSystemdDir); err != nil {
				t.Fatal(err)
			}
			servicePath := filepath.Join(backupSystemdDir, backupServiceUnitName(cfg.Id))
			file, err := os.OpenFile(servicePath, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = file.WriteString("Environment=VEGA_BACKUP_STATE_DIR=" + backupStateDir() + "\nEnvironment=RESTIC_CACHE_DIR=" + filepath.Join(base, "cache") + "\nStandardOutput=tty\nStandardError=inherit\nTTYPath=/dev/console\n")
			file.Close()
			if err != nil {
				t.Fatal(err)
			}
			unmount()
			t.Cleanup(func() {
				exec.Command("systemctl", "stop", backupPathUnitName(cfg.Id), backupServiceUnitName(cfg.Id)).Run()
				exec.Command("umount", mountpoint).Run()
			})
			if err := activateBackupSystemdUnits(cfg); err != nil {
				t.Fatal(err)
			}
			service := backupServiceUnitName(cfg.Id)
			time.Sleep(7 * time.Second)
			if _, err := os.Stat(filepath.Join(mountpoint, "repo")); !os.IsNotExist(err) {
				t.Fatal("unmounted destination was created on the root filesystem")
			}
			if command("systemctl", "is-active", service) != "active" {
				t.Fatal("watcher did not wait for the mount")
			}
			mount()
			// Missing source forces a real restic failure after initialization.
			wait := func(check func() bool) {
				t.Helper()
				deadline := time.Now().Add(120 * time.Second)
				for time.Now().Before(deadline) {
					if check() {
						return
					}
					time.Sleep(time.Second)
				}
				t.Fatal("timed out waiting for backup state")
			}
			repo := filepath.Join(mountpoint, "repo")
			wait(func() bool { _, err := os.Stat(filepath.Join(repo, "config")); return err == nil })
			time.Sleep(7 * time.Second)
			pid := command("systemctl", "show", "--property=MainPID", "--value", service)
			restoreFixture(t, cfg.Paths[0], "payload", "VM backup data")
			time.Sleep(7 * time.Second)
			rows, _ := os.ReadDir(filepath.Join(repo, "snapshots"))
			if len(rows) != 0 || command("systemctl", "show", "--property=MainPID", "--value", service) != pid {
				t.Fatal("failed backup retried or service restarted while connected")
			}
			// Fast remount without waiting for the watcher to observe absence.
			unmount()
			mount()
			wait(func() bool { rows, _ := os.ReadDir(filepath.Join(repo, "snapshots")); return len(rows) == 1 })
			time.Sleep(7 * time.Second)
			command("systemctl", "restart", service)
			time.Sleep(7 * time.Second)
			rows, _ = os.ReadDir(filepath.Join(repo, "snapshots"))
			if len(rows) != 1 {
				t.Fatalf("connection duplicated after restart: %d snapshots", len(rows))
			}
			if err := removeBackupSystemdUnits(cfg.Id); err != nil {
				t.Fatal(err)
			}
			if command("systemctl", "show", "--property=MainPID", "--value", service) != "0" {
				t.Fatal("deleted schedule left the watcher running")
			}
			if err := withMountedBackup(cfg, func(resolved BackupConfig, _ backupMount) error {
				command("umount", "--lazy", mountpoint)
				return os.WriteFile(filepath.Join(resolved.Destination, "anchored-after-unmount"), []byte("still on the volume"), 0o600)
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(repo); !os.IsNotExist(err) {
				t.Fatal("lazy unmount redirected repository writes onto the root filesystem")
			}
			mount()
			assertRestoreFile(t, repo, "anchored-after-unmount", "still on the volume")
		})
	}
}
