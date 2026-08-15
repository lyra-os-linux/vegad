package dbusserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPersistUpdateStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "status.json")
	want := UpdateStatus{CheckedAt: "2026-08-15T12:00:00Z", Profile: "desktop", NativeCount: 2, FlatpakCount: 1, TotalCount: 3}
	if err := persistUpdateStatus(path, want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got UpdateStatus
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("status = %#v, want %#v", got, want)
	}
	loaded, err := readUpdateStatus(path)
	if err != nil || loaded != want {
		t.Fatalf("read status = %#v, %v", loaded, err)
	}
}

func TestUpdateStatusChangedIgnoresTimestamp(t *testing.T) {
	a := UpdateStatus{CheckedAt: "old", Profile: "desktop", NativeCount: 1, TotalCount: 1}
	b := a
	b.CheckedAt = "new"
	if updateStatusChanged(a, b) {
		t.Fatal("timestamp-only update must not emit a duplicate alert")
	}
	b.TotalCount = 0
	b.NativeCount = 0
	if !updateStatusChanged(a, b) {
		t.Fatal("transition to zero updates must emit an alert")
	}
	a = b
	b.InProgress = true
	if !updateStatusChanged(a, b) {
		t.Fatal("progress transition must emit a state event")
	}
	a = b
	b.InProgress = false
	if updateResultChanged(a, b) {
		t.Fatal("progress-only transition must not duplicate the availability alert")
	}
}
