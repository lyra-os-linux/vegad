package dbusserver

import "testing"

func TestParseSnapperSnapshotsCSV(t *testing.T) {
	input := "number;date;time;type;description\n" +
		"12;2026-07-06;20:10:00;single;before update\n" +
		"13;2026-07-06;20:12:00;post;after update\n"

	rows, err := parseSnapperSnapshots(input)
	if err != nil {
		t.Fatalf("parseSnapperSnapshots: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0].Id != 12 || rows[1].Id != 13 {
		t.Fatalf("expected input ordering, got %+v", rows)
	}
	if rows[0].Trigger != "single" || rows[0].Description != "before update" {
		t.Fatalf("unexpected row mapping: %+v", rows[0])
	}
}
