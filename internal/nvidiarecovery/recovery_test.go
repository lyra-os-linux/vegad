package nvidiarecovery

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreNeverAcceptsUnconfirmedRequest(t *testing.T) {
	if err := RestoreOffline("/", "../../bad", false); err == nil {
		t.Fatal("unconfirmed restoration accepted")
	}
}

func TestInvalidReferencesNeverReachFilesystem(t *testing.T) {
	for _, ref := range []string{"", "../point", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("A", 32), "/tmp/point"} {
		if _, err := load("/nonexistent", ref); err == nil || !strings.Contains(err.Error(), "invalid recovery reference") {
			t.Fatalf("%q: %v", ref, err)
		}
	}
}

func TestRecoveryRecordRejectsWrongStrategyAndIncompletePoint(t *testing.T) {
	for _, r := range []Record{{"restic-offline", "1", "ready"}, {"snapper", "0", "ready"}, {"snapper", "1", "incomplete"}, {"arbitrary", strings.Repeat("a", 32), "installed"}, {"restic-offline", strings.Repeat("a", 32), "unknown"}} {
		if r.valid() {
			t.Fatalf("accepted %+v", r)
		}
	}
	for _, r := range []Record{{"snapper", "12", "failed"}, {"restic-offline", strings.Repeat("a", 32), "restoring"}} {
		if !r.valid() {
			t.Fatalf("rejected %+v", r)
		}
	}
}

func TestOnlyESPIsAllowedInsideBackupMounts(t *testing.T) {
	for _, path := range []string{"/usr", "/usr/local", "/etc/test", "/boot", "/boot/efi/nested", "/var/lib/alternatives", "/var/lib/vegad-nvidia/points"} {
		if !protectedSourceMount(path) {
			t.Fatal(path)
		}
	}
	for _, path := range []string{"/boot/efi", "/home", "/var/lib/postgresql", "/usr-sibling"} {
		if protectedSourceMount(path) {
			t.Fatal(path)
		}
	}
}

func TestProgressLogRetainsSummaryWithinBound(t *testing.T) {
	var b tailBuffer
	b.Write(bytes.Repeat([]byte("x"), 500000))
	b.Write([]byte("\n{\"message_type\":\"summary\"}\n"))
	if len(b.data) > 256*1024 || !bytes.HasSuffix(b.data, []byte("\n{\"message_type\":\"summary\"}\n")) {
		t.Fatal("summary lost or log unbounded")
	}
}

func TestAtomicRecordAndBoundedRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record")
	if err := atomicFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := atomicFile(path, []byte("updated"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := readLimited(path, 7)
	if err != nil || string(data) != "updated" {
		t.Fatalf("%q %v", data, err)
	}
	if _, err = readLimited(path, 6); err == nil {
		t.Fatal("oversized record accepted")
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0600 {
		t.Fatal("confidential record permissions")
	}
	files, _ := os.ReadDir(filepath.Dir(path))
	if len(files) != 1 {
		t.Fatal("temporary file leaked")
	}
}
