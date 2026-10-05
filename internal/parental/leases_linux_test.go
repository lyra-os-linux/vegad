package parental

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fileFixture struct {
	backend *FileLeases
	planner *Planner
	guard   *Guard
	plan    AdministrativePlan
	root    string
	locks   []string
}

func inventoryRow(identity Identity) map[string]any {
	producers := map[string]any{}
	for _, name := range requiredProducers {
		producers[name] = map[string]any{"state": "registered", "adapter_digest": strings.Repeat("c", 64), "routes": []any{map[string]any{"id": "fixture-" + name, "digest": strings.Repeat("d", 64)}}}
	}
	raw, _ := json.Marshal(identity)
	var id any
	_ = json.Unmarshal(raw, &id)
	return map[string]any{"schema": 1, "identity": id, "revision": 1, "producers": producers}
}

// JSON identity proof is intentionally a fixture codec, not the PAM format.
func fixtureProof(data []byte, uid uint32) (Identity, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	raw, err := readJSON(d, 0)
	if err != nil {
		return Identity{}, InvalidIdentity
	}
	if _, err = d.Token(); err != io.EOF {
		return Identity{}, InvalidIdentity
	}
	identity, ok := parseIdentity(raw)
	if !ok || identity.UID != uid {
		return Identity{}, InvalidIdentity
	}
	return identity, nil
}
func writePrivate(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func newFileFixture(t *testing.T) *fileFixture {
	t.Helper()
	root := t.TempDir()
	paths := map[string]string{}
	for _, part := range []string{"catalog", "inventory", "ledger"} {
		paths[part] = filepath.Join(root, part)
		if err := os.Mkdir(paths[part], 0700); err != nil {
			t.Fatal(err)
		}
	}
	writePrivate(t, filepath.Join(paths["catalog"], "catalog.json"), encodeRow(t, fixtureRow()))
	writePrivate(t, filepath.Join(paths["catalog"], "catalog.lock"), nil)
	// A second UID is preserved without being the subject of any operation.
	for _, uid := range []uint32{1002, 1000} {
		id, _ := observe(uid)
		u := "1002"
		if uid == 1000 {
			u = "1000"
		}
		writePrivate(t, filepath.Join(paths["inventory"], u+".json"), encodeRow(t, inventoryRow(id)))
		writePrivate(t, filepath.Join(paths["inventory"], ".inventory-"+u+".lock"), nil)
		data, _ := json.Marshal(id)
		writePrivate(t, filepath.Join(paths["ledger"], u), data)
		writePrivate(t, filepath.Join(paths["ledger"], ".gate-"+u+".lock"), nil)
	}
	b := NewFileLeases(paths["catalog"], paths["inventory"], paths["ledger"], fixtureProof)
	b.owner = uint32(os.Geteuid())
	r := NewReader(paths["catalog"])
	r.owner = b.owner
	p := NewPlanner(r, func(string) (uint32, error) { return 1000, nil }, func(uint32, string) (bool, error) { return true, nil }, observe)
	g := NewGuard(p, b, groupResolver)
	plan, err := g.Prepare(":1.20", strings.Repeat("a", 32), 1, grantInput())
	if err != nil {
		t.Fatal(err)
	}
	locks := []string{filepath.Join(paths["catalog"], "catalog.lock"), filepath.Join(paths["inventory"], ".inventory-1002.lock"), filepath.Join(paths["ledger"], ".gate-1002.lock")}
	return &fileFixture{b, p, g, plan, root, locks}
}
func probe(t *testing.T, path string, mode int, wantBusy bool) {
	t.Helper()
	fd, err := unix.Open(path, unix.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	err = unix.Flock(fd, mode|unix.LOCK_NB)
	if wantBusy {
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			t.Fatal("expected lock contention", path, err)
		}
	} else if err != nil {
		t.Fatal("leaked lock", path, err)
	}
}
func assertReleased(t *testing.T, x *fileFixture) {
	t.Helper()
	for _, path := range x.locks {
		probe(t, path, unix.LOCK_EX, false)
	}
}
func filesImage(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var stat unix.Stat_t
			if err = unix.Stat(path, &stat); err != nil {
				return err
			}
			out[path] = fmt.Sprintf("%x|%o|%d|%d|%d|%d|%d", data, stat.Mode, stat.Uid, stat.Gid, stat.Ino, stat.Mtim.Sec, stat.Mtim.Nsec)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestFileLeasesHoldRealLocksAndNeverWrite(t *testing.T) {
	x := newFileFixture(t)
	before := filesImage(t, x.root)
	for _, exclusive := range []bool{false, true} {
		calls := 0
		err := x.backend.WithLease(x.plan.admission, exclusive, func(state MutationState) error {
			calls++
			for _, path := range x.locks {
				probe(t, path, unix.LOCK_EX, true)
			}
			probe(t, x.locks[2], unix.LOCK_SH, exclusive)
			if state.Proof == nil || state.Proof.UID != 1002 || !hash.MatchString(state.InventoryDigest) || !hash.MatchString(state.LedgerDigest) {
				t.Fatal("invalid snapshot")
			}
			return nil
		})
		if err != nil || calls != 1 {
			t.Fatal(err, calls)
		}
		assertReleased(t, x)
	}
	after := filesImage(t, x.root)
	if len(before) != len(after) {
		t.Fatal("reader created state")
	}
	for path, image := range before {
		if after[path] != image {
			t.Fatal("reader changed file or peer", path)
		}
	}
}
func TestFileLeasesIntegrateGuardWithRealFinalExclusiveLease(t *testing.T) {
	x := newFileFixture(t)
	auth := 0
	effects := 0
	result, err := x.guard.Execute(":1.20", x.plan.Identifier(), func(AuthorizationRequest) (bool, error) { auth++; assertReleased(t, x); return true, nil }, func(Method, Account, Parameters) error {
		effects++
		for _, path := range x.locks {
			probe(t, path, unix.LOCK_EX, true)
		}
		probe(t, x.locks[2], unix.LOCK_SH, true)
		return nil
	})
	if err != nil || result != "OrdinaryMutationDispatched" || auth != 1 || effects != 1 {
		t.Fatal(result, err)
	}
	assertReleased(t, x)
	_, err = x.guard.Execute(":1.20", x.plan.Identifier(), func(AuthorizationRequest) (bool, error) { t.Fatal("replay authorization"); return true, nil }, func(Method, Account, Parameters) error { t.Fatal("replayed effect"); return nil })
	assertCode(t, err, Denied)
}
func TestFileLeasesContentionReleasesPreviouslyAcquiredLocks(t *testing.T) {
	for n := 0; n < 3; n++ {
		t.Run(string(rune('a'+n)), func(t *testing.T) {
			x := newFileFixture(t)
			fd, err := unix.Open(x.locks[n], unix.O_RDONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			called := false
			err = x.backend.WithLease(x.plan.admission, true, func(MutationState) error { called = true; return nil })
			assertCode(t, err, Busy)
			if called {
				t.Fatal("busy visitor called")
			}
			for i, path := range x.locks {
				if i != n {
					probe(t, path, unix.LOCK_EX, false)
				}
			}
		})
	}
}
func TestFileLeasesNegativePresenceWinsOverPositiveParsing(t *testing.T) {
	for _, name := range []string{".deny-1002", ".withdrawal-1002.json", ".operation-1002.json"} {
		for _, kind := range []string{"corrupt", "symlink", "fifo"} {
			t.Run(name+kind, func(t *testing.T) {
				x := newFileFixture(t)
				path := filepath.Join(x.backend.ledger, name)
				switch kind {
				case "symlink":
					if err := os.Symlink("absent", path); err != nil {
						t.Fatal(err)
					}
				case "fifo":
					if err := unix.Mkfifo(path, 0600); err != nil {
						t.Fatal(err)
					}
				default:
					writePrivate(t, path, []byte("garbage"))
				}
				// Even corrupt inventory and a proof codec that must not run cannot bypass it.
				writePrivate(t, filepath.Join(x.backend.inventory, "1002.json"), []byte("invalid"))
				x.backend.decodeProof = func([]byte, uint32) (Identity, error) {
					t.Fatal("decoded positive proof before negative overlay")
					return Identity{}, nil
				}
				err := x.backend.WithLease(x.plan.admission, true, func(MutationState) error { t.Fatal("negative state visited"); return nil })
				assertCode(t, err, Blocked)
				assertReleased(t, x)
			})
		}
	}
}
func TestFileLeasesMarkerDuringAuthorizationPreventsEffect(t *testing.T) {
	x := newFileFixture(t)
	effects := 0
	_, err := x.guard.Execute(":1.20", x.plan.Identifier(), func(AuthorizationRequest) (bool, error) {
		assertReleased(t, x)
		writePrivate(t, filepath.Join(x.backend.ledger, ".deny-1002"), []byte("intent"))
		return true, nil
	}, func(Method, Account, Parameters) error { effects++; return nil })
	assertCode(t, err, Blocked)
	if effects != 0 {
		t.Fatal("negative overlay ignored")
	}
	assertReleased(t, x)
}
func TestFileLeasesRejectUnsafeStateAndMissingCanonicals(t *testing.T) {
	for _, relative := range []string{"inventory/.inventory-1002.lock", "inventory/1002.json", "ledger/.gate-1002.lock", "ledger/1002"} {
		for _, mode := range []string{"missing", "symlink", "hardlink", "permissions", "oversize"} {
			t.Run(relative+mode, func(t *testing.T) {
				x := newFileFixture(t)
				path := filepath.Join(x.root, relative)
				switch mode {
				case "missing":
					if err := os.Rename(path, path+".tmp"); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					if err := os.Rename(path, path+".real"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(path+".real", path); err != nil {
						t.Fatal(err)
					}
				case "hardlink":
					if err := os.Link(path, path+".extra"); err != nil {
						t.Fatal(err)
					}
				case "permissions":
					if err := os.Chmod(path, 0644); err != nil {
						t.Fatal(err)
					}
				case "oversize":
					if strings.HasSuffix(path, ".lock") {
						return
					}
					writePrivate(t, path, make([]byte, CatalogLimit+1))
				}
				err := x.backend.WithLease(x.plan.admission, true, func(MutationState) error { t.Fatal("unsafe state visited"); return nil })
				want := UnsafeState
				if mode == "missing" {
					want = RecoveryRequired
				}
				assertCode(t, err, want)
				// Paths intentionally made unsafe/missing can't be probed themselves.
				for _, lock := range x.locks {
					if lock != path {
						probe(t, lock, unix.LOCK_EX, false)
					}
				}
			})
		}
	}
}
func TestFileLeasesCodecFailuresAndIdentityBinding(t *testing.T) {
	for _, mode := range []string{"nil", "failed", "wrong identity", "publish marker", "replace lock", "mutate input"} {
		t.Run(mode, func(t *testing.T) {
			x := newFileFixture(t)
			original := x.backend.decodeProof
			if mode == "nil" {
				x.backend.decodeProof = nil
			} else {
				x.backend.decodeProof = func(data []byte, uid uint32) (Identity, error) {
					identity, err := original(data, uid)
					switch mode {
					case "failed":
						return Identity{}, errors.New("private codec data")
					case "wrong identity":
						identity.GID++
					case "publish marker":
						writePrivate(t, filepath.Join(x.backend.ledger, ".operation-1002.json"), []byte("intent"))
					case "replace lock":
						if err := os.Rename(x.locks[2], x.locks[2]+".old"); err != nil {
							t.Fatal(err)
						}
						writePrivate(t, x.locks[2], nil)
					case "mutate input":
						data[0] = 'x'
					}
					return identity, err
				}
			}
			calls := 0
			err := x.backend.WithLease(x.plan.admission, true, func(s MutationState) error {
				calls++
				data, _ := os.ReadFile(filepath.Join(x.backend.ledger, "1002"))
				if s.LedgerDigest != proofDigest(data) {
					t.Fatal("codec changed sealed proof")
				}
				return nil
			})
			switch mode {
			case "nil":
				assertCode(t, err, RecoveryRequired)
			case "failed", "wrong identity":
				assertCode(t, err, InvalidIdentity)
			case "publish marker":
				assertCode(t, err, Blocked)
			case "replace lock":
				assertCode(t, err, UnsafeState)
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode != "mutate input" && calls != 0 {
				t.Fatal("unsafe codec reached visitor")
			}
			assertReleased(t, x)
		})
	}
}
func TestFileLeasesReleaseOnVisitorErrorAndPanic(t *testing.T) {
	x := newFileFixture(t)
	err := x.backend.WithLease(x.plan.admission, true, func(MutationState) error { return Conflict })
	assertCode(t, err, Conflict)
	assertReleased(t, x)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected fixture panic")
			}
		}()
		_ = x.backend.WithLease(x.plan.admission, true, func(MutationState) error { panic("fixture visitor failed") })
	}()
	assertReleased(t, x)
}
func TestFileLeasesProductionOwnerIsRoot(t *testing.T) {
	x := newFileFixture(t)
	b := NewFileLeases(x.backend.catalog, x.backend.inventory, x.backend.ledger, fixtureProof)
	if b.owner != 0 {
		t.Fatal("production owner bypass")
	}
	if os.Geteuid() != 0 {
		err := b.WithLease(x.plan.admission, true, func(MutationState) error { t.Fatal("unprivileged files accepted by production backend"); return nil })
		assertCode(t, err, UnsafeCatalog)
	}
}
