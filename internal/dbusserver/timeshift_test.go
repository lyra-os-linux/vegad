package dbusserver

import "testing"

func TestParseTimeshiftSnapshots(t *testing.T) {
	input := `Device : /dev/sda1
Num     Name                 Tags     Description
------------------------------------------------------------------------------
0    >  2023-03-05_09-39-32  O B D M  First Snapshot
1    >  2024-07-10_18-20-00  O        Before upgrade
`
	rows := parseTimeshiftSnapshots(input)
	if len(rows) != 2 {
		t.Fatalf("esperava 2 snapshots, recebeu %d", len(rows))
	}
	if rows[0].Description != "Before upgrade" || rows[0].Trigger != "O" {
		t.Fatalf("snapshot mais recente incorreto: %#v", rows[0])
	}
	if rows[1].Description != "First Snapshot" || rows[1].Trigger != "O B D M" {
		t.Fatalf("snapshot antigo incorreto: %#v", rows[1])
	}
	if rows[0].Id != timeshiftSnapshotID("2024-07-10_18-20-00") {
		t.Fatalf("ID não é estável")
	}
}
