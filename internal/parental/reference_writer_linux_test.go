package parental

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func referenceWriterFixture(t *testing.T) (*ReferenceWriter, *FileReferenceSource, RecoveryReference, RecoveryReference, string) {
	s, before, dir := fileReferenceFixture(t)
	w := NewReferenceWriter(dir, s.catalogDirectory, s.issuer, s.qualify)
	w.owner = uint32(os.Geteuid())
	return w, s, before, decodeReference(t, referenceRow("blocked-recovery")), dir
}
func maintenanceFixture() error { return nil }
func TestReferenceWriterAdvanceRetainsLockAndRejectsReplay(t *testing.T) {
	w, s, before, next, dir := referenceWriterFixture(t)
	operation := strings.Repeat("2", 32)
	original, _ := os.Stat(filepath.Join(dir, "reference.lock"))
	calls := 0
	revalidate := func() error {
		calls++
		if calls > 1 {
			probe(t, filepath.Join(dir, "reference.lock"), unix.LOCK_SH, true)
		}
		return nil
	}
	ref, e := w.Advance(before, next, operation, revalidate)
	if e != nil || ref.Digest() != next.Digest() {
		t.Fatal(ref, e)
	}
	if calls < 3 {
		t.Fatal("missing fresh maintenance checks", calls)
	}
	got, e := s.Snapshot(before.Installation())
	if e != nil || got.Digest() != next.Digest() {
		t.Fatal(got, e)
	}
	after, _ := os.Stat(filepath.Join(dir, "reference.lock"))
	if !os.SameFile(original, after) {
		t.Fatal("lock replaced")
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 3 {
		t.Fatal("residue", files)
	}
	_, e = w.Advance(before, next, operation, maintenanceFixture)
	assertCode(t, e, Conflict)
	_, e = w.Resume(next, operation, maintenanceFixture)
	assertCode(t, e, RecoveryRequired)
}
func TestReferenceWriterCutsRequireExactAuthorizedResume(t *testing.T) {
	for _, cut := range []string{referenceTransactionName + ":durable", "reference.json:durable", "current.json:durable", "journal:removed"} {
		t.Run(cut, func(t *testing.T) {
			w, s, before, next, dir := referenceWriterFixture(t)
			operation := strings.Repeat("2", 32)
			w.checkpoint = func(stage string) {
				if stage == cut {
					panic("process-cut fixture")
				}
			}
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("missing cut")
					}
				}()
				w.Advance(before, next, operation, maintenanceFixture)
			}()
			w.checkpoint = nil
			if cut == "journal:removed" {
				got, e := s.Snapshot(before.Installation())
				if e != nil || got.Digest() != next.Digest() {
					t.Fatal(got, e)
				}
				return
			}
			_, e := s.Snapshot(before.Installation())
			assertCode(t, e, RecoveryRequired)
			_, e = w.Advance(before, next, operation, maintenanceFixture)
			assertCode(t, e, RecoveryRequired)
			_, e = w.Resume(next, strings.Repeat("3", 32), maintenanceFixture)
			assertCode(t, e, Conflict)
			_, e = w.Resume(next, operation, nil)
			assertCode(t, e, Denied)
			journalBefore, _ := os.ReadFile(filepath.Join(dir, referenceTransactionName))
			recovered, e := w.Resume(next, operation, maintenanceFixture)
			if e != nil || recovered.Digest() != next.Digest() {
				t.Fatal(recovered, e)
			}
			if len(journalBefore) == 0 {
				t.Fatal("missing durable journal")
			}
			got, e := s.Snapshot(before.Installation())
			if e != nil || got.Digest() != next.Digest() {
				t.Fatal(got, e)
			}
			files, _ := os.ReadDir(dir)
			if len(files) != 3 {
				t.Fatal("residue", files)
			}
		})
	}
}
func TestReferenceWriterRefusesUnsafeMissingAndRevoked(t *testing.T) {
	for _, mode := range []string{"missing-lock", "missing-pin", "missing-reference", "pin-symlink", "pin-hardlink", "pin-fifo", "pin-permissions", "wrong-issuer", "unqualified", "boundary-change", "nil-check", "revoked", "busy", "pending-unsafe", "stale", "default-root"} {
		t.Run(mode, func(t *testing.T) {
			w, s, before, next, dir := referenceWriterFixture(t)
			check := maintenanceFixture
			pin := filepath.Join(dir, "current.json")
			want := RecoveryRequired
			held := -1
			switch mode {
			case "missing-lock":
				os.Remove(filepath.Join(dir, "reference.lock"))
			case "missing-pin":
				os.Remove(pin)
			case "missing-reference":
				os.Remove(filepath.Join(dir, "reference.json"))
			case "pin-symlink":
				os.Rename(pin, pin+"-old")
				os.Symlink(pin+"-old", pin)
				want = UnsafeState
			case "pin-hardlink":
				os.Link(pin, pin+"-copy")
				want = UnsafeState
			case "pin-fifo":
				os.Remove(pin)
				unix.Mkfifo(pin, 0600)
				want = UnsafeState
			case "pin-permissions":
				os.Chmod(pin, 0644)
				want = UnsafeState
			case "wrong-issuer":
				w.issuer = strings.Repeat("3", 32)
				want = Conflict
			case "unqualified":
				w.qualify = func(string, string) (string, error) { return "", RecoveryRequired }
			case "boundary-change":
				calls := 0
				w.qualify = func(string, string) (string, error) { calls++; return strings.Repeat(string(rune('a'+calls)), 64), nil }
				want = Conflict
			case "nil-check":
				check = nil
				want = Denied
			case "revoked":
				calls := 0
				check = func() error {
					calls++
					if calls > 1 {
						return Denied
					}
					return nil
				}
				want = Denied
			case "busy":
				held, _ = unix.Open(filepath.Join(dir, "reference.lock"), unix.O_RDONLY, 0)
				unix.Flock(held, unix.LOCK_SH)
				defer unix.Close(held)
				want = Busy
			case "pending-unsafe":
				os.Symlink("missing", filepath.Join(dir, referenceTransactionName))
			case "stale":
				before = next
				want = Conflict
			case "default-root":
				if os.Geteuid() == 0 {
					t.Skip("unprivileged test")
				}
				w = NewReferenceWriter(dir, s.catalogDirectory, s.issuer, s.qualify)
				want = UnsafeState
			}
			old, _ := os.ReadFile(filepath.Join(dir, "reference.json"))
			_, e := w.Advance(before, next, strings.Repeat("2", 32), check)
			assertCode(t, e, want)
			after, _ := os.ReadFile(filepath.Join(dir, "reference.json"))
			if !bytes.Equal(old, after) {
				t.Fatal("refusal changed reference")
			}
		})
	}
}
func TestReferenceTransactionSchemaAndOwnership(t *testing.T) {
	w, _, before, next, _ := referenceWriterFixture(t)
	_ = w
	transaction := referenceTransaction{strings.Repeat("f", 32), strings.Repeat("2", 32), before, next}
	good, e := transactionBytes(transaction)
	if e != nil {
		t.Fatal(e)
	}
	for _, data := range [][]byte{nil, bytes.Repeat([]byte("x"), referenceTransactionLimit+1), append(good, []byte("{}")...), bytes.Replace(good, []byte(`"schema":1`), []byte(`"schema":1,"schema":1`), 1), bytes.Replace(good, []byte(`"schema":1`), []byte(`"schema":null`), 1), bytes.Replace(good, []byte(`"schema":1`), []byte(`"extra":1,"schema":1`), 1)} {
		if _, e := decodeReferenceTransaction(data); e == nil {
			t.Fatal("accepted invalid journal")
		}
	}
	decoded, e := decodeReferenceTransaction(good)
	if e != nil || decoded.after.Digest() != next.Digest() {
		t.Fatal(decoded, e)
	}
	row := referenceRow("blocked-recovery")
	cat := row["catalog"].(map[string]any)
	first(cat)["generation"] = strings.Repeat("9", 32)
	changed := decodeReference(t, row)
	if referenceAdvance(before, changed) {
		t.Fatal("ownership replaced")
	}
	if referenceAdvance(next, before) || referenceAdvance(before, before) {
		t.Fatal("non-monotone advance")
	}
	// Storage denied before invoking the topology adapter.
	w.qualify = func(string, string) (string, error) { t.Fatal("unauthorized storage access"); return "", nil }
	_, e = w.Advance(before, next, strings.Repeat("2", 32), func() error { return errors.New("private denial") })
	assertCode(t, e, Denied)
}
func TestReferenceWriterDurabilityFailureBlocksUntilResume(t *testing.T) {
	for _, failAt := range []int{1, 2, 3, 4, 5, 6, 7, 8} {
		t.Run(string(rune('a'+failAt)), func(t *testing.T) {
			w, s, before, next, dir := referenceWriterFixture(t)
			calls := 0
			w.sync = func(fd int) error {
				calls++
				if calls == failAt {
					return errors.New("disk failure")
				}
				return unix.Fsync(fd)
			}
			_, e := w.Advance(before, next, strings.Repeat("2", 32), maintenanceFixture)
			if e == nil {
				t.Fatal("missing failure", failAt, calls)
			}
			w.sync = unix.Fsync
			if failAt == 1 {
				assertCode(t, e, StorageFailure)
				got, e := s.Snapshot(before.Installation())
				if e != nil || got.Digest() != before.Digest() {
					t.Fatal(got, e)
				}
				return
			}
			assertCode(t, e, PublicationUncertain)
			if _, e := os.Lstat(filepath.Join(dir, referenceTransactionName)); os.IsNotExist(e) {
				got, e := s.Snapshot(before.Installation())
				if e != nil || got.Digest() != next.Digest() {
					t.Fatal(got, e)
				}
				return
			}
			_, e = s.Snapshot(before.Installation())
			assertCode(t, e, RecoveryRequired)
			_, e = w.Resume(next, strings.Repeat("2", 32), maintenanceFixture)
			if e != nil {
				t.Fatal(e)
			}
		})
	}
}
