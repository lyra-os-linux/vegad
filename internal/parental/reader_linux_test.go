package parental

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func readerFixture(t *testing.T) (*Reader, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"catalog.lock": {}, "catalog.json": encodeRow(t, fixtureRow())} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return &Reader{directory: dir, owner: uint32(os.Geteuid())}, dir
}
func TestReaderSnapshotAndNoWrites(t *testing.T) {
	r, dir := readerFixture(t)
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	contents := map[string][]byte{}
	stats := map[string]unix.Stat_t{}
	for _, e := range before {
		path := filepath.Join(dir, e.Name())
		contents[e.Name()], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var s unix.Stat_t
		if unix.Stat(path, &s) != nil {
			t.Fatal("stat")
		}
		stats[e.Name()] = s
	}
	c, err := r.Snapshot()
	if err != nil || c.Revision() != 1 {
		t.Fatal(c, err)
	}
	for name, data := range contents {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != string(data) {
			t.Fatal("reader wrote content", name, err)
		}
		var now unix.Stat_t
		if unix.Stat(filepath.Join(dir, name), &now) != nil {
			t.Fatal("stat")
		}
		old := stats[name]
		if old.Ino != now.Ino || old.Mtim != now.Mtim || old.Ctim != now.Ctim || old.Mode != now.Mode {
			t.Fatal("reader changed metadata", name)
		}
	}
	after, err := os.ReadDir(dir)
	if err != nil || len(before) != len(after) {
		t.Fatal("reader created files", err)
	}
}
func TestReaderRequiresCanonicalExistingFiles(t *testing.T) {
	for _, name := range []string{"catalog.lock", "catalog.json"} {
		t.Run(name, func(t *testing.T) {
			r, dir := readerFixture(t)
			if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, name+".temporary")); err != nil {
				t.Fatal(err)
			}
			_, err := r.Snapshot()
			assertCode(t, err, RecoveryRequired)
			if _, err = os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
				t.Fatal("reader created missing canonical file", err)
			}
		})
	}
	r, dir := readerFixture(t)
	r.directory = filepath.Join(dir, "absent")
	_, err := r.Snapshot()
	assertCode(t, err, RecoveryRequired)
}
func TestReaderRejectsUnsafeFiles(t *testing.T) {
	changes := map[string]func(*testing.T, string){
		"public directory": func(t *testing.T, d string) {
			if err := os.Chmod(d, 0755); err != nil {
				t.Fatal(err)
			}
		},
		"public catalog": func(t *testing.T, d string) {
			if err := os.Chmod(filepath.Join(d, "catalog.json"), 0644); err != nil {
				t.Fatal(err)
			}
		},
		"public lock": func(t *testing.T, d string) {
			if err := os.Chmod(filepath.Join(d, "catalog.lock"), 0644); err != nil {
				t.Fatal(err)
			}
		},
		"empty": func(t *testing.T, d string) {
			if err := os.WriteFile(filepath.Join(d, "catalog.json"), nil, 0600); err != nil {
				t.Fatal(err)
			}
		},
		"oversize": func(t *testing.T, d string) {
			if err := os.WriteFile(filepath.Join(d, "catalog.json"), make([]byte, CatalogLimit+1), 0600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for _, name := range []string{"catalog.json", "catalog.lock"} {
		name := name
		changes[name+" symlink"] = func(t *testing.T, d string) {
			p := filepath.Join(d, name)
			if err := os.Rename(p, p+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(p+".real", p); err != nil {
				t.Fatal(err)
			}
		}
		changes[name+" hardlink"] = func(t *testing.T, d string) {
			if err := os.Link(filepath.Join(d, name), filepath.Join(d, "extra-link")); err != nil {
				t.Fatal(err)
			}
		}
		changes[name+" fifo"] = func(t *testing.T, d string) {
			p := filepath.Join(d, name)
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(p, 0600); err != nil {
				t.Fatal(err)
			}
		}
		changes[name+" directory"] = func(t *testing.T, d string) {
			p := filepath.Join(d, name)
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(p, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			r, dir := readerFixture(t)
			change(t, dir)
			_, err := r.Snapshot()
			assertCode(t, err, UnsafeCatalog)
		})
	}
	r, dir := readerFixture(t)
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	r.directory = link
	_, err := r.Snapshot()
	assertCode(t, err, UnsafeCatalog)
	r, _ = readerFixture(t)
	r.owner++
	_, err = r.Snapshot()
	assertCode(t, err, UnsafeCatalog)
	if NewReader(dir).owner != 0 {
		t.Fatal("production constructor accepts nonroot owner")
	}
}
func TestReaderBusyAndReleasesLease(t *testing.T) {
	r, dir := readerFixture(t)
	fd, err := unix.Open(filepath.Join(dir, "catalog.lock"), unix.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	_, err = r.Snapshot()
	assertCode(t, err, Busy)
	if err := unix.Flock(fd, unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal("reader leaked lease", err)
	}
}
func TestReaderRejectsMalformedCatalogAndReleasesLease(t *testing.T) {
	r, dir := readerFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), []byte(`{"schema":1,"schema":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := r.Snapshot()
	assertCode(t, err, InvalidCatalog)
	fd, err := unix.Open(filepath.Join(dir, "catalog.lock"), unix.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal("invalid snapshot leaked lock", err)
	}
}
func TestReaderDetectsReplacedLockInode(t *testing.T) {
	_, dir := readerFixture(t)
	p := filepath.Join(dir, "catalog.lock")
	fd, err := unix.Open(p, unix.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	var s unix.Stat_t
	if err = unix.Fstat(fd, &s); err != nil {
		t.Fatal(err)
	}
	dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(dfd)
	if !sameEntry(dfd, "catalog.lock", &s) {
		t.Fatal("initial inode mismatch")
	}
	if err = os.Rename(p, p+".old"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if sameEntry(dfd, "catalog.lock", &s) {
		t.Fatal("replacement inode accepted")
	}
}
