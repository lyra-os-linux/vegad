package parental

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerConsumptionRootDisposableVM(t *testing.T) {
	if os.Getenv("LYRA_PARENTAL_INSTALLER_VM") != "1" {
		t.Skip("requires disposable Btrfs guest")
	}
	const base = "/root/lyra-go-installer-proof-test"
	const volume = base + "/volume"
	marker, _ := os.ReadFile("/proc/cmdline")
	serial, _ := os.ReadFile("/sys/block/vdb/serial")
	mountinfo, _ := os.ReadFile("/proc/self/mountinfo")
	if os.Geteuid() != 0 || !strings.Contains(string(marker), "lyra.parental-identity-test=1") || strings.TrimSpace(string(serial)) != "lyra-ref-domain-test" || !strings.Contains(string(mountinfo), " "+volume+" ") || !strings.Contains(string(mountinfo), " - btrfs /dev/vdb ") {
		t.Fatal("disposable disk guard")
	}
	scope := volume + "/root"
	catalog := scope + "/catalog"
	issuer := strings.Repeat("f", 32)
	operation := strings.Repeat("2", 32)
	initial := decodeReference(t, referenceRow("first-install"))
	next := decodeReference(t, referenceRow("blocked-recovery"))
	qualifier := NewBtrfsSnapshotDomain(scope).Qualify
	// This durable receipt is a fixture, outside both catalog and reference
	// subvolumes. It is not produced by the actual Rust installer yet.
	newProvisioner := func(authority string) *ReferenceProvisioner {
		proof := volume + "/proof-" + strings.TrimPrefix(authority, volume+"/authority-")
		check := func(installation, op, digest string) error {
			if installation != initial.installation || op != operation || digest != initial.digest {
				return Denied
			}
			if _, e := os.Lstat(proof); os.IsNotExist(e) {
				return nil
			}
			data, e := os.ReadFile(proof)
			if e != nil {
				return Denied
			}
			var receipt map[string]string
			if json.Unmarshal(data, &receipt) == nil && receipt["installation"] == installation && receipt["operation"] == op && receipt["state"] == "consumed" && receipt["digest"] == digest {
				return InstallerPermissionConsumed
			}
			return Denied
		}
		consume := func(installation, op, digest string) error {
			if check(installation, op, digest) != nil {
				return Denied
			}
			f, e := os.OpenFile(proof, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				return e
			}
			data, _ := json.Marshal(map[string]string{"installation": installation, "operation": op, "state": "consumed", "digest": digest})
			if _, e = f.Write(data); e != nil {
				f.Close()
				return e
			}
			if e = f.Sync(); e != nil {
				f.Close()
				return e
			}
			if e = f.Close(); e != nil {
				return e
			}
			d, e := os.Open(volume)
			if e != nil {
				return e
			}
			defer d.Close()
			return d.Sync()
		}
		return NewInstallerReferenceProvisioner(authority, catalog, issuer, qualifier, check, consume)
	}
	if stage := os.Getenv("LYRA_PARENTAL_INSTALLER_CUT"); stage != "" {
		authority := os.Getenv("LYRA_PARENTAL_INSTALLER_AUTHORITY")
		if !strings.HasPrefix(authority, volume+"/authority-") || filepath.Clean(authority) != authority {
			t.Fatal("child path guard")
		}
		p := newProvisioner(authority)
		p.writer.checkpoint = func(p string) {
			if p == stage {
				os.Exit(73)
			}
		}
		if _, e := p.Initialize(initial, operation, maintenanceFixture); e != nil {
			t.Fatal(e)
		}
		t.Fatal("cut not reached")
	}
	run := func(binary string, args ...string) {
		t.Helper()
		out, e := exec.Command(binary, args...).CombinedOutput()
		if e != nil {
			t.Fatal(binary, args, e, string(out))
		}
	}
	if _, e := os.Lstat(scope); !os.IsNotExist(e) {
		t.Fatal("not fresh")
	}
	run("/usr/sbin/btrfs", "subvolume", "create", scope)
	os.Chmod(scope, 0700)
	run("mount", "--bind", scope, scope)
	if e := os.Mkdir(catalog, 0700); e != nil {
		t.Fatal(e)
	}
	writePrivate(t, catalog+"/catalog.json", encodeRow(t, fixtureRow()))
	writePrivate(t, catalog+"/catalog.lock", nil)
	catalogBefore, _ := os.ReadFile(catalog + "/catalog.json")
	cases := []string{}
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	for i, stage := range []string{enrollmentName + ":durable", "reference.json:durable", "current.json:durable", enrollmentCommitName + ":durable", "installer:consumed"} {
		authority := fmt.Sprintf("%s/authority-%d", volume, i)
		run("/usr/sbin/btrfs", "subvolume", "create", authority)
		os.Chmod(authority, 0700)
		run("mount", "--bind", authority, authority)
		writePrivate(t, authority+"/reference.lock", nil)
		lockBefore, _ := os.Stat(authority + "/reference.lock")
		child := exec.Command(exe, "-test.run", "^TestInstallerConsumptionRootDisposableVM$", "-test.timeout", "40s")
		child.Env = append(os.Environ(), "LYRA_PARENTAL_INSTALLER_CUT="+stage, "LYRA_PARENTAL_INSTALLER_AUTHORITY="+authority)
		out, e := child.CombinedOutput()
		exit, ok := e.(*exec.ExitError)
		if !ok || exit.ExitCode() != 73 {
			t.Fatal("process cut", stage, e, string(out))
		}
		source := NewFileReferenceSource(authority, catalog, issuer, qualifier)
		p := newProvisioner(authority)
		_, e = p.Initialize(initial, operation, maintenanceFixture)
		want := Conflict
		if stage == enrollmentCommitName+":durable" || stage == "installer:consumed" {
			want = Denied
		}
		assertCode(t, e, want)
		if stage == "installer:consumed" {
			_, e = source.Snapshot(initial.installation)
			assertCode(t, e, RecoveryRequired)
			got, resumeErr := p.ResumeInitial(initial, operation, maintenanceFixture)
			if resumeErr != nil || got.Digest() != initial.Digest() {
				t.Fatal(got, resumeErr)
			}
			if _, e = os.Lstat(authority + "/" + enrollmentCommitName); e != nil {
				t.Fatal("missing commit after authorized consumed resume", e)
			}
			if _, e = source.Snapshot(initial.installation); e != nil {
				t.Fatal("resumed reference is not readable", e)
			}
			cases = append(cases, stage)
			continue
		}
		if stage != enrollmentCommitName+":durable" {
			_, e = source.Snapshot(initial.Installation())
			assertCode(t, e, RecoveryRequired)
			_, e = p.ResumeInitial(initial, operation, nil)
			assertCode(t, e, Denied)
			_, e = p.ResumeInitial(initial, strings.Repeat("3", 32), maintenanceFixture)
			assertCode(t, e, Denied)
			fresh := p.proveFresh
			p.proveFresh = func(string, string, string) error { return Denied }
			_, e = p.ResumeInitial(initial, operation, maintenanceFixture)
			assertCode(t, e, Denied)
			p.proveFresh = fresh
			got, e := p.ResumeInitial(initial, operation, maintenanceFixture)
			if e != nil || got.Digest() != initial.Digest() {
				t.Fatal(got, e)
			}
		} else {
			_, e = p.ResumeInitial(initial, operation, maintenanceFixture)
			assertCode(t, e, Conflict)
		}
		got, e := source.Snapshot(initial.Installation())
		if e != nil || got.Digest() != initial.Digest() {
			t.Fatal(got, e)
		}
		seal, e := os.ReadFile(authority + "/" + enrollmentName)
		if e != nil {
			t.Fatal(e)
		}
		commit, e := os.ReadFile(authority + "/" + enrollmentCommitName)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = p.writer.Advance(initial, next, operation, maintenanceFixture); e != nil {
			t.Fatal(e)
		}
		got, e = source.Snapshot(next.Installation())
		if e != nil || got.Digest() != next.Digest() {
			t.Fatal(got, e)
		}
		for name, want := range map[string][]byte{enrollmentName: seal, enrollmentCommitName: commit} {
			actual, e := os.ReadFile(authority + "/" + name)
			if e != nil || !bytes.Equal(actual, want) {
				t.Fatal("marker changed", name, e)
			}
		}
		lockAfter, _ := os.Stat(authority + "/reference.lock")
		if !os.SameFile(lockBefore, lockAfter) {
			t.Fatal("lock replaced")
		}
		entries, _ := os.ReadDir(authority)
		if len(entries) != 5 {
			t.Fatal("unexpected entries", entries)
		}
		lost := [][]string{{"reference.json"}, {"current.json"}, {"reference.json", "current.json"}, {enrollmentName, enrollmentCommitName, "reference.json", "current.json"}}[i]
		for _, name := range lost {
			if e = os.Remove(authority + "/" + name); e != nil {
				t.Fatal(e)
			}
		}
		if _, e = source.Snapshot(initial.Installation()); e == nil {
			t.Fatal("missing authority accepted")
		}
		if _, e = p.Initialize(initial, operation, maintenanceFixture); e == nil {
			t.Fatal("reseeded lost authority")
		}
		if _, e = p.ResumeInitial(initial, operation, maintenanceFixture); e == nil {
			t.Fatal("repaired completed authority")
		}
		for _, name := range lost {
			if _, e = os.Lstat(authority + "/" + name); !os.IsNotExist(e) {
				t.Fatal("recreated", name, e)
			}
		}
		cases = append(cases, stage)
	}
	catalogAfter, _ := os.ReadFile(catalog + "/catalog.json")
	if !bytes.Equal(catalogBefore, catalogAfter) {
		t.Fatal("issuer touched catalog")
	}
	result := map[string]any{"passed": true, "cases": cases, "actual_process_cuts": true, "default_root_factory": true, "actual_btrfs_qualifier": true, "pending_reader_refused": true, "unauthorized_resume_refused": true, "wrong_operation_refused": true, "same_lock_retained": true, "exact_resume_completed": true, "catalog_unchanged": true, "only_canonical_entries": true, "completed_loss_not_reseeded": true, "permanent_markers_retained": true, "revoked_installer_proof_refused": true, "installer_proof_fixture": true, "durable_consumption_fixture": true, "consumed_cut_fail_closed": true, "all_local_markers_lost_still_denied": true, "permission_consumed_before_commit": true, "issuer_fixture": true, "maintenance_fixture": true, "production_integrated": false, "provisioning_implemented": true}
	data, _ := json.MarshalIndent(result, "", "  ")
	if e := os.WriteFile(base+"/go-result.json", append(data, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
}
