package dbusserver

import (
	"context"
	"os"
	"testing"
	"time"
)

// A destination that cannot be listed (disk unplugged, restic missing)
// falls back to the last success vegad recorded.
func TestBackupAgesFallBackToTheRecordedSuccess(t *testing.T) {
	t.Setenv("VEGA_BACKUP_STATE_DIR", t.TempDir())
	t.Setenv("PATH", t.TempDir()) // no restic: listing fails
	if err := os.MkdirAll(backupConfigsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeBackupConfig(backupConfigPath("usb"), BackupConfig{Id: "usb", Paths: []string{"/home"}, Destination: "/nonexistent/repo", Frequency: "manual"}); err != nil {
		t.Fatal(err)
	}
	ages, err := backupAges(context.Background())
	if err != nil || len(ages) != 1 || !ages[0].Disconnected || !ages[0].Last.IsZero() {
		t.Fatalf("before any backup: %+v %v", ages, err)
	}
	when := time.Unix(1793000000, 0)
	if err := recordBackupSuccess("usb", when); err != nil {
		t.Fatal(err)
	}
	ages, _ = backupAges(context.Background())
	if !ages[0].Last.Equal(when) {
		t.Fatalf("after a backup: %+v", ages)
	}
}
