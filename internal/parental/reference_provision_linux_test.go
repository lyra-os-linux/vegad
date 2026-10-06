package parental

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// All issuer, topology and installer attestations here are explicit fixtures.
func provisionFixture(t *testing.T) (*ReferenceProvisioner, *FileReferenceSource, RecoveryReference, string, string) {
	t.Helper()
	s, initial, dir := fileReferenceFixture(t)
	for _, name := range []string{"current.json", "reference.json"} {
		if e := os.Remove(filepath.Join(dir, name)); e != nil {
			t.Fatal(e)
		}
	}
	op := strings.Repeat("2", 32)
	p := NewReferenceProvisioner(dir, s.catalogDirectory, s.issuer, s.qualify, func(installation, operation, digest string) error {
		if installation != initial.installation || operation != op || digest != initial.digest {
			return Denied
		}
		return nil
	})
	p.writer.owner = uint32(os.Geteuid())
	return p, s, initial, dir, op
}

func TestReferenceProvisionInitialAndAdvance(t *testing.T) {
	p, s, initial, dir, op := provisionFixture(t)
	lock, _ := os.Stat(filepath.Join(dir, "reference.lock"))
	got, e := p.Initialize(initial, op, maintenanceFixture)
	if e != nil || got.digest != initial.digest {
		t.Fatal(got, e)
	}
	got, e = s.Snapshot(initial.installation)
	if e != nil || got.digest != initial.digest {
		t.Fatal(got, e)
	}
	seal, _ := os.ReadFile(filepath.Join(dir, enrollmentName))
	commit, _ := os.ReadFile(filepath.Join(dir, enrollmentCommitName))
	next := decodeReference(t, referenceRow("blocked-recovery"))
	if _, e = p.writer.Advance(initial, next, op, maintenanceFixture); e != nil {
		t.Fatal(e)
	}
	got, e = s.Snapshot(initial.installation)
	if e != nil || got.digest != next.digest {
		t.Fatal(got, e)
	}
	for name, want := range map[string][]byte{enrollmentName: seal, enrollmentCommitName: commit} {
		actual, _ := os.ReadFile(filepath.Join(dir, name))
		if !bytes.Equal(actual, want) {
			t.Fatal("permanent marker changed", name)
		}
	}
	after, _ := os.Stat(filepath.Join(dir, "reference.lock"))
	if !os.SameFile(lock, after) {
		t.Fatal("lock replaced")
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 5 {
		t.Fatal("unexpected entries", files)
	}
}

func cutProvision(t *testing.T, p *ReferenceProvisioner, initial RecoveryReference, op, cut string) {
	t.Helper()
	p.writer.checkpoint = func(stage string) {
		if stage == cut {
			panic("fixture interruption")
		}
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("missing cut", cut)
			}
		}()
		p.Initialize(initial, op, maintenanceFixture)
	}()
	p.writer.checkpoint = nil
}

func TestReferenceProvisionCutsAndExactResume(t *testing.T) {
	for _, cut := range []string{enrollmentName + ":durable", "reference.json:durable", "current.json:durable", enrollmentCommitName + ":durable"} {
		t.Run(cut, func(t *testing.T) {
			p, s, initial, dir, op := provisionFixture(t)
			cutProvision(t, p, initial, op, cut)
			_, e := p.Initialize(initial, op, maintenanceFixture)
			assertCode(t, e, Conflict)
			if cut == enrollmentCommitName+":durable" {
				if _, e = s.Snapshot(initial.installation); e != nil {
					t.Fatal(e)
				}
				_, e = p.ResumeInitial(initial, op, maintenanceFixture)
				assertCode(t, e, Conflict)
				return
			}
			_, e = s.Snapshot(initial.installation)
			assertCode(t, e, RecoveryRequired)
			_, e = p.writer.Advance(initial, decodeReference(t, referenceRow("blocked-recovery")), op, maintenanceFixture)
			assertCode(t, e, RecoveryRequired)
			_, e = p.ResumeInitial(initial, op, nil)
			assertCode(t, e, Denied)
			// Allow a different proof fixture to reach the durable operation check.
			proof := p.proveFresh
			p.proveFresh = func(string, string, string) error { return nil }
			_, e = p.ResumeInitial(initial, strings.Repeat("3", 32), maintenanceFixture)
			assertCode(t, e, Conflict)
			p.proveFresh = proof
			got, e := p.ResumeInitial(initial, op, maintenanceFixture)
			if e != nil || got.digest != initial.digest {
				t.Fatal(got, e)
			}
			if _, e = s.Snapshot(initial.installation); e != nil {
				t.Fatal(e)
			}
			files, _ := os.ReadDir(dir)
			if len(files) != 5 {
				t.Fatal(files)
			}
		})
	}
}

func TestReferenceProvisionNeverRepairsCompletedLoss(t *testing.T) {
	for _, lost := range []string{"reference.json", "current.json", "both", "reference.lock"} {
		t.Run(lost, func(t *testing.T) {
			p, s, initial, dir, op := provisionFixture(t)
			if _, e := p.Initialize(initial, op, maintenanceFixture); e != nil {
				t.Fatal(e)
			}
			paths := []string{lost}
			if lost == "both" {
				paths = []string{"reference.json", "current.json"}
			}
			for _, name := range paths {
				if e := os.Remove(filepath.Join(dir, name)); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := s.Snapshot(initial.installation); e == nil {
				t.Fatal("accepted missing authority")
			}
			for _, call := range []func(RecoveryReference, string, func() error) (RecoveryReference, error){p.Initialize, p.ResumeInitial} {
				if _, e := call(initial, op, maintenanceFixture); e == nil {
					t.Fatal("reseeded completed installation")
				}
			}
			for _, name := range paths {
				if _, e := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(e) {
					t.Fatal("recreated", name, e)
				}
			}
		})
	}
}

func TestReferenceProvisionRefusesUnprovenOrOccupied(t *testing.T) {
	for _, mode := range []string{"nil-proof", "not-fresh", "nil-maintenance", "missing-lock", "legacy", "orphan", "busy", "unsafe-lock", "revoked-proof"} {
		t.Run(mode, func(t *testing.T) {
			p, _, initial, dir, op := provisionFixture(t)
			check, want := maintenanceFixture, Denied
			switch mode {
			case "nil-proof":
				p.proveFresh = nil
			case "not-fresh":
				p.proveFresh = func(string, string, string) error { return Denied }
			case "nil-maintenance":
				check = nil
			case "missing-lock":
				os.Remove(filepath.Join(dir, "reference.lock"))
				want = RecoveryRequired
			case "legacy":
				data, _ := referenceBytes(initial)
				writePrivate(t, filepath.Join(dir, "reference.json"), data)
				want = Conflict
			case "orphan":
				writePrivate(t, filepath.Join(dir, ".reference-orphan.tmp"), nil)
				want = Conflict
			case "busy":
				fd, e := unix.Open(filepath.Join(dir, "reference.lock"), unix.O_RDONLY, 0)
				if e != nil {
					t.Fatal(e)
				}
				defer unix.Close(fd)
				unix.Flock(fd, unix.LOCK_SH)
				want = Busy
			case "unsafe-lock":
				os.Chmod(filepath.Join(dir, "reference.lock"), 0644)
				want = UnsafeState
			case "revoked-proof":
				calls := 0
				p.proveFresh = func(string, string, string) error {
					calls++
					if calls > 1 {
						return Denied
					}
					return nil
				}
			}
			if mode == "nil-proof" || mode == "not-fresh" || mode == "nil-maintenance" {
				p.writer.qualify = func(string, string) (string, error) { t.Fatal("storage before authorization"); return "", nil }
			}
			_, e := p.Initialize(initial, op, check)
			assertCode(t, e, want)
			if _, e = os.Lstat(filepath.Join(dir, enrollmentName)); !os.IsNotExist(e) {
				t.Fatal("published despite refusal", e)
			}
		})
	}
}

func TestReferenceProvisionDurabilityFailures(t *testing.T) {
	for failAt := 1; failAt <= 8; failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			p, s, initial, dir, op := provisionFixture(t)
			calls := 0
			p.writer.sync = func(fd int) error {
				calls++
				if calls == failAt {
					return errors.New("fixture disk failure")
				}
				return unix.Fsync(fd)
			}
			got, e := p.Initialize(initial, op, maintenanceFixture)
			want := PublicationUncertain
			if failAt == 1 {
				want = StorageFailure
			}
			assertCode(t, e, want)
			if got.digest != "" {
				t.Fatal("acknowledged failed publication")
			}
			p.writer.sync = unix.Fsync
			if failAt == 1 {
				_, e = p.Initialize(initial, op, maintenanceFixture)
			} else if failAt == 8 {
				// Rename is visible but directory durability was not acknowledged.
				_, e = p.ResumeInitial(initial, op, maintenanceFixture)
				assertCode(t, e, Conflict)
				_, e = s.Snapshot(initial.installation)
			} else {
				_, e = s.Snapshot(initial.installation)
				assertCode(t, e, RecoveryRequired)
				_, e = p.ResumeInitial(initial, op, maintenanceFixture)
			}
			if e != nil {
				t.Fatal(e)
			}
			files, _ := os.ReadDir(dir)
			if len(files) != 5 {
				t.Fatal(files)
			}
		})
	}
}

func TestReferenceEnrollmentStrictMarkers(t *testing.T) {
	p, _, initial, _, op := provisionFixture(t)
	good, e := enrollmentBytes(enrollment{p.writer.issuer, op, initial})
	if e != nil {
		t.Fatal(e)
	}
	for _, data := range [][]byte{nil, bytes.Repeat([]byte("x"), enrollmentLimit+1), append(good, []byte("{}")...), bytes.Replace(good, []byte(`"schema":1`), []byte(`"schema":1,"schema":1`), 1), bytes.Replace(good, []byte(`"schema":1`), []byte(`"schema":null`), 1), bytes.Replace(good, []byte(`"schema":1`), []byte(`"extra":1,"schema":1`), 1), {0xff}} {
		if _, e := decodeEnrollment(data); e == nil {
			t.Fatal("accepted malformed seal")
		}
	}
	for _, mode := range []string{"missing-seal", "missing-commit", "symlink", "hardlink", "fifo", "permissions", "corrupt", "mismatch"} {
		t.Run(mode, func(t *testing.T) {
			p, s, initial, dir, op := provisionFixture(t)
			if _, e := p.Initialize(initial, op, maintenanceFixture); e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(dir, enrollmentCommitName)
			switch mode {
			case "missing-seal":
				os.Remove(filepath.Join(dir, enrollmentName))
			case "missing-commit":
				os.Remove(path)
			case "symlink":
				os.Rename(path, path+"-old")
				os.Symlink(path+"-old", path)
			case "hardlink":
				os.Link(path, path+"-copy")
			case "fifo":
				os.Remove(path)
				unix.Mkfifo(path, 0600)
			case "permissions":
				os.Chmod(path, 0644)
			case "corrupt":
				writePrivate(t, path, []byte(`{}`))
			case "mismatch":
				data, _ := os.ReadFile(path)
				writePrivate(t, path, bytes.Replace(data, []byte(initial.digest), []byte(strings.Repeat("0", 64)), 1))
			}
			if _, e := s.Snapshot(initial.installation); e == nil {
				t.Fatal("accepted incomplete or unsafe enrollment")
			}
		})
	}
}

// A real child exits without running defer, leaving the OS to release EX.
// These are process-interruption tests on fixture storage, not VM/Btrfs or
// power-loss qualifications. The installer and domain checks remain fixtures.
func TestReferenceProvisionProcessChild(t *testing.T) {
	dir := os.Getenv("LYRA_PROVISION_PROCESS_DIR")
	if dir == "" {
		t.Skip("child helper")
	}
	initial := decodeReference(t, referenceRow("first-install"))
	p := NewReferenceProvisioner(dir, filepath.Join(dir, "../catalog"), strings.Repeat("f", 32), func(string, string) (string, error) { return strings.Repeat("a", 64), nil }, func(string, string, string) error { return nil })
	p.writer.owner = uint32(os.Geteuid())
	p.writer.checkpoint = func(stage string) {
		if stage == os.Getenv("LYRA_PROVISION_PROCESS_CUT") {
			os.Exit(73)
		}
	}
	_, e := p.Initialize(initial, strings.Repeat("2", 32), maintenanceFixture)
	t.Fatal("did not reach process cut", e)
}

func TestReferenceProvisionProcessCuts(t *testing.T) {
	for _, cut := range []string{enrollmentName + ":durable", "reference.json:durable", "current.json:durable", enrollmentCommitName + ":durable"} {
		t.Run(cut, func(t *testing.T) {
			p, s, initial, dir, op := provisionFixture(t)
			binary, e := os.Executable()
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestReferenceProvisionProcessChild$")
			cmd.Env = append(os.Environ(), "LYRA_PROVISION_PROCESS_DIR="+dir, "LYRA_PROVISION_PROCESS_CUT="+cut)
			out, e := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(e, &exit) || exit.ExitCode() != 73 {
				t.Fatalf("cut failed: %v %s", e, out)
			}
			probe(t, filepath.Join(dir, "reference.lock"), unix.LOCK_EX, false)
			_, e = s.Snapshot(initial.installation)
			if cut == enrollmentCommitName+":durable" {
				if e != nil {
					t.Fatal(e)
				}
				_, e = p.ResumeInitial(initial, op, maintenanceFixture)
				assertCode(t, e, Conflict)
			} else {
				assertCode(t, e, RecoveryRequired)
				if _, e = p.ResumeInitial(initial, op, maintenanceFixture); e != nil {
					t.Fatal(e)
				}
				if _, e = s.Snapshot(initial.installation); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}

func TestReferenceProvisionPendingRefusesIncompatibleState(t *testing.T) {
	for _, mode := range []string{"pin-without-reference", "different-reference", "update-journal", "revoked-proof", "revoked-maintenance"} {
		t.Run(mode, func(t *testing.T) {
			p, s, initial, dir, op := provisionFixture(t)
			cutProvision(t, p, initial, op, enrollmentName+":durable")
			switch mode {
			case "pin-without-reference":
				writePrivate(t, filepath.Join(dir, "current.json"), referencePinBytes(initial, p.writer.issuer))
			case "different-reference":
				next := decodeReference(t, referenceRow("blocked-recovery"))
				data, _ := referenceBytes(next)
				writePrivate(t, filepath.Join(dir, "reference.json"), data)
			case "update-journal":
				os.Symlink("missing", filepath.Join(dir, referenceTransactionName))
			case "revoked-proof":
				p.proveFresh = func(string, string, string) error { return Denied }
			}
			check := maintenanceFixture
			if mode == "revoked-maintenance" {
				check = func() error { return Denied }
			}
			if _, e := p.ResumeInitial(initial, op, check); e == nil {
				t.Fatal("accepted incompatible resume")
			}
			if _, e := s.Snapshot(initial.installation); e == nil {
				t.Fatal("pending accepted")
			}
			if _, e := os.Lstat(filepath.Join(dir, enrollmentCommitName)); !os.IsNotExist(e) {
				t.Fatal("completed refused transaction", e)
			}
		})
	}
}
