package dbusserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTimeshiftSnapshots(t *testing.T) {
	// Shape of `timeshift --list` output (Num, ">", Name, Tags, Description
	// columns, per src/Console/AppConsole.vala's list_snapshots).
	input := "Device : /dev/sda2\n" +
		"UUID   : c1a2b3c4-d5e6-f708-1234-56789abcdef0\n" +
		"Path   : /timeshift\n" +
		"Mode   : RSYNC\n\n" +
		"Num     Name                 Tags  Description\n" +
		"------------------------------------------------------------------------\n" +
		"0     >  2026-07-06_20-10-00  O B D M  antes da atualização\n" +
		"1     >  2026-07-07_08-30-00  D     \n"

	rows := parseTimeshiftSnapshots(input)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d: %+v", len(rows), rows)
	}

	// Sorted newest-first, like listSnapperSnapshots.
	if rows[0].Trigger != "daily" || rows[0].Description != "" {
		t.Fatalf("unexpected newest row: %+v", rows[0])
	}
	if rows[1].Trigger != "manual" || rows[1].Description != "antes da atualização" {
		t.Fatalf("unexpected oldest row: %+v", rows[1])
	}

	wantID, err := timeshiftID("2026-07-06_20-10-00")
	if err != nil {
		t.Fatalf("timeshiftID: %v", err)
	}
	if rows[1].Id != wantID {
		t.Fatalf("expected id %d, got %d", wantID, rows[1].Id)
	}
}

func TestTimeshiftConfigConfigured(t *testing.T) {
	tests := []struct {
		name string
		json string
		want bool
	}{
		{name: "rsync device", json: `{"backup_device_uuid":"abc","do_first_run":"false"}`, want: true},
		{name: "configured btrfs without uuid", json: `{"btrfs_mode":"true","do_first_run":"false"}`, want: true},
		{name: "first run string", json: `{"backup_device_uuid":"","do_first_run":"true"}`, want: false},
		{name: "first run bool", json: `{"backup_device_uuid":"","do_first_run":true}`, want: false},
		{name: "invalid first run value", json: `{"backup_device_uuid":"","do_first_run":"unknown"}`, want: false},
		{name: "invalid json", json: `{`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := timeshiftConfigConfigured([]byte(tt.json)); got != tt.want {
				t.Fatalf("timeshiftConfigConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFindTimeshiftConfigPathFromSupportsLegacyFallback(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "timeshift.json")
	if err := os.WriteFile(legacy, []byte(`{"do_first_run":"false"}`), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	got, ok := findTimeshiftConfigPathFrom([]string{filepath.Join(dir, "missing.json"), legacy})
	if !ok || got != legacy {
		t.Fatalf("findTimeshiftConfigPathFrom() = %q, %v; want %q, true", got, ok, legacy)
	}
}

func TestTimeshiftSnapshotCountDetection(t *testing.T) {
	if !timeshiftSnapshotCountRe.MatchString("3 snapshots, 198.9 GB free") {
		t.Fatal("expected non-empty Timeshift summary to be detected")
	}
	if timeshiftSnapshotCountRe.MatchString("0 snapshots, 198.9 GB free") {
		t.Fatal("zero snapshots must not be treated as a parser failure")
	}
}

func TestListTimeshiftSnapshotsUsesCLIAsSourceOfTruth(t *testing.T) {
	installFakeTimeshift(t, "1 snapshots, 100 GB free\n0 > 2026-07-13_10-30-45 O backup real\n")

	rows, err := listTimeshiftSnapshots()
	if err != nil {
		t.Fatalf("listTimeshiftSnapshots: %v", err)
	}
	if len(rows) != 1 || rows[0].Description != "backup real" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}

func TestListTimeshiftSnapshotsReportsParserDrift(t *testing.T) {
	installFakeTimeshift(t, "2 snapshots, 100 GB free\nnew unsupported table layout\n")

	if _, err := listTimeshiftSnapshots(); err == nil {
		t.Fatal("expected parser drift to be reported instead of returning an empty list")
	}
}

func installFakeTimeshift(t *testing.T, output string) {
	t.Helper()
	dir := t.TempDir()
	command := filepath.Join(dir, "timeshift")
	script := "#!/bin/sh\nprintf '%s' " + shellSingleQuote(output) + "\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake timeshift: %v", err)
	}
	t.Setenv("PATH", dir)
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func TestTimeshiftIDDeterministic(t *testing.T) {
	a, err := timeshiftID("2026-07-06_20-10-00")
	if err != nil {
		t.Fatalf("timeshiftID: %v", err)
	}
	b, err := timeshiftID("2026-07-06_20-10-00")
	if err != nil {
		t.Fatalf("timeshiftID: %v", err)
	}
	if a != b {
		t.Fatalf("expected same name to yield the same id, got %d and %d", a, b)
	}

	c, err := timeshiftID("2026-07-07_08-30-00")
	if err != nil {
		t.Fatalf("timeshiftID: %v", err)
	}
	if a == c {
		t.Fatalf("expected different names to yield different ids")
	}
}
