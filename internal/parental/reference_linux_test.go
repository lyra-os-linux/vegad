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

func pinRow(ref RecoveryReference) map[string]any {
	return map[string]any{"schema": 1, "installation": ref.Installation(), "issuer": strings.Repeat("f", 32), "sequence": ref.Sequence(), "reference_digest": ref.Digest()}
}
func fileReferenceFixture(t *testing.T) (*FileReferenceSource, RecoveryReference, string) {
	t.Helper()
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	ref := decodeReference(t, referenceRow("first-install"))
	writePrivate(t, filepath.Join(dir, "reference.lock"), nil)
	writePrivate(t, filepath.Join(dir, "current.json"), encodeRow(t, pinRow(ref)))
	data, _ := referenceBytes(ref)
	writePrivate(t, filepath.Join(dir, "reference.json"), data)
	source := NewFileReferenceSource(dir, filepath.Join(t.TempDir(), "catalog"), strings.Repeat("f", 32), func(authority, catalog string) (string, error) {
		if authority != dir || catalog == authority {
			t.Fatal("unexpected domain")
		}
		return strings.Repeat("a", 64), nil
	})
	source.owner = uint32(os.Geteuid()) // private owner override only; witness is a fixture
	return source, ref, dir
}
func TestFileReferenceSourcePinnedSnapshotAndReadOnly(t *testing.T) {
	s, ref, dir := fileReferenceFixture(t)
	stats := map[string]os.FileInfo{}
	contents := map[string][]byte{}
	for _, n := range []string{"reference.lock", "current.json", "reference.json"} {
		stats[n], _ = os.Stat(filepath.Join(dir, n))
		contents[n], _ = os.ReadFile(filepath.Join(dir, n))
	}
	got, e := s.Snapshot(ref.Installation())
	if e != nil || got.Digest() != ref.Digest() {
		t.Fatal(got, e)
	}
	// SH reference lease is held during both topology observations and released.
	calls := 0
	s.qualify = func(string, string) (string, error) {
		calls++
		if calls == 2 {
			probe(t, filepath.Join(dir, "reference.lock"), unix.LOCK_EX, true)
		}
		return strings.Repeat("a", 64), nil
	}
	_, e = s.Snapshot(ref.Installation())
	if e != nil || calls != 2 {
		t.Fatal(e, calls)
	}
	probe(t, filepath.Join(dir, "reference.lock"), unix.LOCK_EX, false)
	for n, before := range contents {
		after, _ := os.ReadFile(filepath.Join(dir, n))
		info, _ := os.Stat(filepath.Join(dir, n))
		if !bytes.Equal(before, after) || !os.SameFile(stats[n], info) || stats[n].ModTime() != info.ModTime() {
			t.Fatal("reader changed state", n)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Fatal("created state")
	}
}
func TestFileReferenceSourceRefusesMissingUnsafeAndUnqualified(t *testing.T) {
	for _, mode := range []string{"nil-witness", "unqualified", "witness-change", "same-domain", "relative", "wrong-issuer", "wrong-installation", "wrong-sequence", "old-reference", "missing-pin", "missing-reference", "missing-lock", "invalid-pin", "invalid-reference", "permissions", "symlink", "hardlink", "fifo", "contention", "default-root"} {
		t.Run(mode, func(t *testing.T) {
			s, ref, dir := fileReferenceFixture(t)
			want := RecoveryRequired
			current := filepath.Join(dir, "current.json")
			record := filepath.Join(dir, "reference.json")
			lock := filepath.Join(dir, "reference.lock")
			switch mode {
			case "nil-witness":
				s.qualify = nil
			case "unqualified":
				s.qualify = func(string, string) (string, error) { return "", errors.New("private topology failure") }
			case "witness-change":
				calls := 0
				s.qualify = func(string, string) (string, error) { calls++; return strings.Repeat(string(rune('a'+calls)), 64), nil }
				want = Conflict
			case "same-domain":
				s.catalogDirectory = s.directory
			case "relative":
				s.directory = "relative"
			case "wrong-issuer":
				s.issuer = strings.Repeat("e", 32)
				want = Conflict
			case "wrong-installation":
				want = Conflict
			case "wrong-sequence":
				row := pinRow(ref)
				row["sequence"] = 2
				writePrivate(t, current, encodeRow(t, row))
				want = Conflict
			case "old-reference":
				newRef := decodeReference(t, referenceRow("blocked-recovery"))
				writePrivate(t, current, encodeRow(t, pinRow(newRef)))
				want = Conflict
			case "missing-pin":
				os.Remove(current)
			case "missing-reference":
				os.Remove(record)
			case "missing-lock":
				os.Remove(lock)
			case "invalid-pin":
				writePrivate(t, current, []byte("{}"))
				want = InvalidReference
			case "invalid-reference":
				writePrivate(t, record, []byte("{}"))
				want = InvalidReference
			case "permissions":
				os.Chmod(current, 0644)
				want = UnsafeState
			case "symlink":
				os.Rename(record, filepath.Join(dir, "original"))
				os.Symlink("original", record)
				want = UnsafeState
			case "hardlink":
				os.Link(record, filepath.Join(dir, "link"))
				want = UnsafeState
			case "fifo":
				os.Remove(record)
				if e := unix.Mkfifo(record, 0600); e != nil {
					t.Fatal(e)
				}
				want = UnsafeState
			case "contention":
				fd, e := unix.Open(lock, unix.O_RDONLY, 0)
				if e != nil {
					t.Fatal(e)
				}
				defer unix.Close(fd)
				if e = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); e != nil {
					t.Fatal(e)
				}
				want = Busy
			case "default-root":
				if os.Geteuid() == 0 {
					t.Skip("requires unprivileged fixture")
				}
				s.owner = 0
				want = UnsafeState
			}
			installation := ref.Installation()
			if mode == "wrong-installation" {
				installation = strings.Repeat("2", 32)
			}
			_, e := s.Snapshot(installation)
			assertCode(t, e, want)
			if mode == "missing-pin" || mode == "missing-reference" || mode == "missing-lock" {
				entries, _ := os.ReadDir(dir)
				if len(entries) != 2 {
					t.Fatal("recreated missing state")
				}
			}
		})
	}
}
func TestReferencePinClosedSchema(t *testing.T) {
	ref := decodeReference(t, referenceRow("first-install"))
	for _, mode := range []string{"unknown", "null", "float", "issuer", "digest", "schema"} {
		row := pinRow(ref)
		switch mode {
		case "unknown":
			row["extra"] = true
		case "null":
			row["issuer"] = nil
		case "float":
			row["sequence"] = 1.5
		case "issuer":
			row["issuer"] = "root"
		case "digest":
			row["reference_digest"] = "old"
		case "schema":
			row["schema"] = 2
		}
		_, e := decodeReferencePin(encodeRow(t, row))
		assertCode(t, e, InvalidReference)
	}
	data := encodeRow(t, pinRow(ref))
	for _, bad := range [][]byte{nil, bytes.Repeat([]byte("a"), referencePinLimit+1), append(append([]byte{}, data...), []byte("{}")...), bytes.Replace(data, []byte(`"schema":1`), []byte(`"schema":1,"schema":1`), 1)} {
		_, e := decodeReferencePin(bad)
		assertCode(t, e, InvalidReference)
	}
}

func TestFileReferenceSourceComposesWithInspector(t *testing.T) {
	s, ref, _ := fileReferenceFixture(t)
	local := &planSource{err: RecoveryRequired}
	inspector := NewRecoveryInspector(s, local, func(string) (uint32, error) { return 1000, nil }, func(uint32, string) (bool, error) { return true, nil }, observe)
	a, e := inspector.Inspect(":1.20", ref.Installation())
	if e != nil || a.Disposition() != BootstrapRequired || a.ReferenceDigest() != ref.Digest() {
		t.Fatal(a, e)
	}
	s.qualify = nil
	_, e = inspector.Inspect(":1.20", ref.Installation())
	assertCode(t, e, RecoveryRequired)
}
