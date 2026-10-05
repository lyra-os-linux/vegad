package parental

import (
	"bytes"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func writerFixture(t *testing.T) (*CatalogWriter, Catalog, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	row := fixtureRow()
	extra := map[string]any{"association": strings.Repeat("c", 32), "generation": strings.Repeat("d", 32), "identity": Identity{"peer", 1003, 100, "users"}, "revision": 1, "kind": "ordinary", "state": "active", "operation": "", "barrier": false, "policy": "", "adapters": map[string]any{}}
	row["accounts"] = append(row["accounts"].([]any), extra)
	writePrivate(t, filepath.Join(dir, "catalog.json"), encodeRow(t, row))
	writePrivate(t, filepath.Join(dir, "catalog.lock"), nil)
	w := NewCatalogWriter(dir)
	w.owner = uint32(os.Geteuid())
	c := decodeRow(t, row)
	return w, c, dir
}
func readWriter(t *testing.T, w *CatalogWriter) Catalog {
	t.Helper()
	r := NewReader(w.directory)
	r.owner = w.owner
	c, e := r.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestCatalogWriterBlocksOnlySelectedOrdinary(t *testing.T) {
	w, c, dir := writerFixture(t)
	association := strings.Repeat("a", 32)
	operation := strings.Repeat("e", 32)
	lockInfo, _ := os.Stat(filepath.Join(dir, "catalog.lock"))
	prior := c.Accounts()
	next, e := w.BlockOrdinary(c, association, operation)
	if e != nil {
		t.Fatal(e)
	}
	if next.Revision() != 2 || next.Digest() == c.Digest() || readWriter(t, w).Digest() != next.Digest() {
		t.Fatal("publication", next)
	}
	target := next.Accounts()[0]
	if target.State != "recovering" || !target.Barrier || target.Operation != operation || target.Revision != 2 || target.Identity != prior[0].Identity || target.Generation != prior[0].Generation || target.Association != association {
		t.Fatal(target)
	}
	if !reflect.DeepEqual(next.Accounts()[1], prior[1]) || !reflect.DeepEqual(c.Accounts(), prior) {
		t.Fatal("peer/source changed")
	}
	_, e = Classify(next, 1002, observe, nil, nil)
	assertCode(t, e, Blocked)
	if eligible, e := Classify(next, 1003, func(uint32) (Identity, error) { return prior[1].Identity, nil }, nil, nil); e != nil || eligible != OrdinaryEligible {
		t.Fatal(eligible, e)
	}
	nowInfo, _ := os.Stat(filepath.Join(dir, "catalog.lock"))
	if !os.SameFile(lockInfo, nowInfo) {
		t.Fatal("lock replaced")
	}
	files, e := os.ReadDir(dir)
	if e != nil || len(files) != 2 {
		t.Fatal(files, e)
	}
	beforeInfo, _ := os.Stat(filepath.Join(dir, "catalog.json"))
	again, e := w.BlockOrdinary(next, association, operation)
	if e != nil || again.Digest() != next.Digest() {
		t.Fatal(again, e)
	}
	afterInfo, _ := os.Stat(filepath.Join(dir, "catalog.json"))
	if !os.SameFile(beforeInfo, afterInfo) {
		t.Fatal("noop rewrote")
	}
	_, e = w.BlockOrdinary(c, association, operation)
	assertCode(t, e, Conflict)
	_, e = w.BlockOrdinary(next, association, strings.Repeat("f", 32))
	assertCode(t, e, Blocked)
	_, e = w.BlockOrdinary(next, strings.Repeat("c", 32), operation)
	assertCode(t, e, Conflict)
}
func TestCatalogWriterRefusesUnsafeOrMissingAuthority(t *testing.T) {
	for _, mode := range []string{"missing-catalog", "missing-lock", "missing-dir", "invalid", "symlink", "hardlink", "fifo", "permissions", "default-root", "shared-lock", "stale", "unknown", "supervised", "tombstone", "overflow", "operation-in-use"} {
		t.Run(mode, func(t *testing.T) {
			w, c, dir := writerFixture(t)
			file := filepath.Join(dir, "catalog.json")
			lock := filepath.Join(dir, "catalog.lock")
			before, _ := os.ReadFile(file)
			want := UnsafeCatalog
			var held int = -1
			switch mode {
			case "missing-catalog":
				os.Remove(file)
				want = RecoveryRequired
			case "missing-lock":
				os.Remove(lock)
				want = RecoveryRequired
			case "missing-dir":
				os.RemoveAll(dir)
				want = RecoveryRequired
			case "invalid":
				writePrivate(t, file, []byte("{}"))
				want = InvalidCatalog
			case "symlink":
				os.Rename(file, filepath.Join(dir, "original"))
				os.Symlink("original", file)
			case "hardlink":
				os.Link(file, filepath.Join(dir, "linked"))
			case "fifo":
				os.Remove(file)
				if e := unix.Mkfifo(file, 0600); e != nil {
					t.Fatal(e)
				}
			case "permissions":
				os.Chmod(file, 0644)
			case "default-root":
				if os.Geteuid() == 0 {
					t.Skip("root cannot exercise wrong-owner factory")
				}
				w = NewCatalogWriter(dir)
			case "shared-lock":
				var e error
				held, e = unix.Open(lock, unix.O_RDONLY, 0)
				if e != nil {
					t.Fatal(e)
				}
				defer unix.Close(held)
				if e = unix.Flock(held, unix.LOCK_SH|unix.LOCK_NB); e != nil {
					t.Fatal(e)
				}
				want = Busy
			case "stale":
				c.digest = strings.Repeat("f", 64)
				want = Conflict
			case "unknown":
				want = RecoveryRequired
			case "supervised":
				c.accounts[0].Kind = "supervised"
				c.accounts[0].Policy = strings.Repeat("f", 64)
				c.accounts[0].Adapters = []Adapter{{"fixture", strings.Repeat("e", 64)}}
				data, next, e := encodeCatalog(c)
				if e != nil {
					t.Fatal(e)
				}
				c = next
				writePrivate(t, file, data)
				want = UnsupportedOperation
			case "tombstone":
				c.accounts[0].State = "tombstone"
				c.accounts[0].Operation = strings.Repeat("f", 32)
				data, next, e := encodeCatalog(c)
				if e != nil {
					t.Fatal(e)
				}
				c = next
				writePrivate(t, file, data)
				want = Blocked
			case "overflow":
				c.revision = math.MaxInt64
				data, next, e := encodeCatalog(c)
				if e != nil {
					t.Fatal(e)
				}
				c = next
				writePrivate(t, file, data)
				want = Conflict
			case "operation-in-use":
				c.accounts[1].State = "recovering"
				c.accounts[1].Barrier = true
				c.accounts[1].Operation = strings.Repeat("e", 32)
				data, next, e := encodeCatalog(c)
				if e != nil {
					t.Fatal(e)
				}
				c = next
				writePrivate(t, file, data)
				want = Conflict
			}
			// Preserve the scenario's canonical data rather than the original fixture.
			readable := mode != "fifo" && mode != "missing-catalog" && mode != "missing-dir"
			if readable {
				before, _ = os.ReadFile(file)
			}
			association := strings.Repeat("a", 32)
			if mode == "unknown" {
				association = strings.Repeat("f", 32)
			}
			_, e := w.BlockOrdinary(c, association, strings.Repeat("e", 32))
			assertCode(t, e, want)
			if readable {
				after, _ := os.ReadFile(file)
				if !bytes.Equal(before, after) {
					t.Fatal("failure changed catalog")
				}
			}
			if mode == "missing-catalog" {
				if _, e = os.Lstat(file); !os.IsNotExist(e) {
					t.Fatal("reconstructed catalog")
				}
			}
			if mode == "missing-lock" {
				if _, e = os.Lstat(lock); !os.IsNotExist(e) {
					t.Fatal("created lock")
				}
			}
		})
	}
}
func TestCatalogWriterDurabilityFailures(t *testing.T) {
	for _, stage := range []string{"file-sync", "directory-sync", "lock-replaced", "canonical-changed"} {
		t.Run(stage, func(t *testing.T) {
			w, c, dir := writerFixture(t)
			syncs := 0
			w.sync = func(fd int) error {
				syncs++
				if stage == "file-sync" && syncs == 1 || stage == "directory-sync" && syncs == 2 {
					return errors.New("private fsync failure")
				}
				return unix.Fsync(fd)
			}
			w.checkpoint = func(phase string) {
				if phase != "file-synced" {
					return
				}
				if stage == "lock-replaced" {
					os.Remove(filepath.Join(dir, "catalog.lock"))
					writePrivate(t, filepath.Join(dir, "catalog.lock"), nil)
				}
				if stage == "canonical-changed" {
					row := fixtureRow()
					row["revision"] = 7
					writePrivate(t, filepath.Join(dir, "catalog.json"), encodeRow(t, row))
				}
			}
			_, e := w.BlockOrdinary(c, strings.Repeat("a", 32), strings.Repeat("e", 32))
			want := StorageFailure
			if stage == "directory-sync" {
				want = PublicationUncertain
			}
			if stage == "lock-replaced" {
				want = UnsafeCatalog
			}
			if stage == "canonical-changed" {
				want = Conflict
			}
			assertCode(t, e, want)
			now := readWriter(t, w)
			if stage == "directory-sync" {
				if now.Accounts()[0].State != "recovering" || !now.Accounts()[0].Barrier {
					t.Fatal("uncertain publication reopened")
				}
				w.sync = unix.Fsync
				w.checkpoint = nil
				result, e := w.BlockOrdinary(now, strings.Repeat("a", 32), strings.Repeat("e", 32))
				if e != nil || result.Digest() != now.Digest() {
					t.Fatal("durable blocked retry", e)
				}
			} else if stage != "canonical-changed" && now.Digest() != c.Digest() {
				t.Fatal("failure published")
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 2 {
				t.Fatal("temporary leak", entries)
			}
		})
	}
}
func TestCatalogWriterCrashChild(t *testing.T) {
	dir := os.Getenv("LYRA_CATALOG_WRITER_CRASH_DIR")
	if dir == "" {
		t.Skip("subprocess crash helper")
	}
	w := NewCatalogWriter(dir)
	w.owner = uint32(os.Geteuid())
	c := readWriter(t, w)
	w.checkpoint = func(stage string) {
		if stage == os.Getenv("LYRA_CATALOG_WRITER_CRASH_STAGE") {
			os.Exit(19)
		}
	}
	_, e := w.BlockOrdinary(c, strings.Repeat("a", 32), strings.Repeat("e", 32))
	t.Fatal("missing crash", e)
}
func TestCatalogWriterProcessCuts(t *testing.T) {
	for _, stage := range []string{"file-synced", "renamed", "directory-synced"} {
		t.Run(stage, func(t *testing.T) {
			w, c, dir := writerFixture(t)
			cmd := exec.Command(os.Args[0], "-test.run=^TestCatalogWriterCrashChild$")
			cmd.Env = append(os.Environ(), "LYRA_CATALOG_WRITER_CRASH_DIR="+dir, "LYRA_CATALOG_WRITER_CRASH_STAGE="+stage)
			output, e := cmd.CombinedOutput()
			exit, ok := e.(*exec.ExitError)
			if !ok || exit.ExitCode() != 19 {
				t.Fatal("crash", e, string(output))
			}
			now := readWriter(t, w)
			if stage == "file-synced" {
				if now.Digest() != c.Digest() {
					t.Fatal("temp adopted")
				}
				_, e = w.BlockOrdinary(c, strings.Repeat("a", 32), strings.Repeat("e", 32))
				if e != nil {
					t.Fatal(e)
				}
			} else {
				if now.Accounts()[0].State != "recovering" || !now.Accounts()[0].Barrier {
					t.Fatal("cut reopened")
				}
				_, e = w.BlockOrdinary(c, strings.Repeat("a", 32), strings.Repeat("e", 32))
				assertCode(t, e, Conflict)
				result, e := w.BlockOrdinary(now, strings.Repeat("a", 32), strings.Repeat("e", 32))
				if e != nil || result.Digest() != now.Digest() {
					t.Fatal(e)
				}
			}
			if !reflect.DeepEqual(readWriter(t, w).Accounts()[1], c.Accounts()[1]) {
				t.Fatal("peer changed")
			}
		})
	}
}

func TestCatalogWriterPreservesSupervisedAndTombstonedPeers(t *testing.T) {
	for _, state := range []string{"active", "tombstone"} {
		t.Run(state, func(t *testing.T) {
			w, c, dir := writerFixture(t)
			c.accounts[1].Kind = "supervised"
			c.accounts[1].Policy = strings.Repeat("f", 64)
			c.accounts[1].Adapters = []Adapter{{"fixture", strings.Repeat("b", 64)}}
			c.accounts[1].State = state
			if state == "tombstone" {
				c.accounts[1].Operation = strings.Repeat("f", 32)
				c.accounts[1].Barrier = true
			}
			data, before, e := encodeCatalog(c)
			if e != nil {
				t.Fatal(e)
			}
			writePrivate(t, filepath.Join(dir, "catalog.json"), data)
			next, e := w.BlockOrdinary(before, strings.Repeat("a", 32), strings.Repeat("e", 32))
			if e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(before.Accounts()[1], next.Accounts()[1]) {
				t.Fatal("peer policy/tombstone changed")
			}
		})
	}
}
func TestCatalogWriterNoopStillRequiresDurability(t *testing.T) {
	w, c, _ := writerFixture(t)
	next, e := w.BlockOrdinary(c, strings.Repeat("a", 32), strings.Repeat("e", 32))
	if e != nil {
		t.Fatal(e)
	}
	w.sync = func(int) error { return errors.New("directory inaccessible") }
	_, e = w.BlockOrdinary(next, strings.Repeat("a", 32), strings.Repeat("e", 32))
	assertCode(t, e, PublicationUncertain)
	if readWriter(t, w).Digest() != next.Digest() {
		t.Fatal("retry changed catalog")
	}
}
