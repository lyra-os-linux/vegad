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

// This is snapper's actual --csvout format under LC_ALL=C (comma-delimited,
// "number" header) — the real regression: a naive semicolon-first read
// "succeeds" on comma-delimited text by treating each line as one field,
// so it never falls back to comma and every row gets skipped.
func TestParseSnapperSnapshotsCSVComma(t *testing.T) {
	input := "config,subvolume,number,default,active,type,pre-number,date,user,used-space,cleanup,description,userdata\n" +
		"root,/,0,no,no,single,,,root,,,current,\n" +
		"root,/,103,no,no,single,,2026-06-30 10:00:01,root,,timeline,timeline,\n" +
		"root,/,280,no,no,pre,,2026-07-07 17:09:54,root,,,Atualização completa,\n" +
		"root,/,283,no,no,post,280,2026-07-07 17:09:56,root,,,Atualização completa,\n"

	rows, err := parseSnapperSnapshots(input)
	if err != nil {
		t.Fatalf("parseSnapperSnapshots: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].Id != 0 || rows[1].Id != 103 || rows[2].Id != 280 || rows[3].Id != 283 {
		t.Fatalf("expected input ordering, got %+v", rows)
	}
	if rows[2].Trigger != "pre" || rows[2].Description != "Atualização completa" {
		t.Fatalf("unexpected row mapping: %+v", rows[2])
	}
}
