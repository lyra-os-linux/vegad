package dbusserver

import "testing"

func TestParseTimeshiftSnapshots(t *testing.T) {
	// Shape of `timeshift --list` output (Num, ">", Name, Tags, Description
	// columns, per src/Console/AppConsole.vala's list_snapshots).
	input := "Device : /dev/sda2\n" +
		"UUID   : c1a2b3c4-d5e6-f708-1234-56789abcdef0\n" +
		"Path   : /timeshift\n" +
		"Mode   : RSYNC\n\n" +
		"Num     Name                 Tags  Description\n" +
		"------------------------------------------------------------------------\n" +
		"0     >  2026-07-06_20-10-00  O     antes da atualização\n" +
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
