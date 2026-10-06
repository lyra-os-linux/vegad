package parental

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBtrfsDomainStrictQueriesAndUnqualifiedLayouts(t *testing.T) {
	for _, s := range []string{"", "256", "0256\n", "4\n", "-1\n", "256\n257\n", " 256\n", "18446744073709551616\n"} {
		if _, e := domainTree([]byte(s)); e == nil {
			t.Fatal(s)
		}
	}
	if n, e := domainTree([]byte("256\n")); e != nil || n != 256 {
		t.Fatal(n, e)
	}
	for _, s := range []string{"", "uuid: aaaa", "Label: none  uuid: 00000000-0000-0000-0000-000000000000\n", "Label: none  uuid: ABCDEF12-1234-1234-1234-123456789abc\n"} {
		if _, e := domainUUID([]byte(s)); e == nil {
			t.Fatal(s)
		}
	}
	if uuid, e := domainUUID([]byte("Label: none  uuid: abcdef12-1234-1234-1234-123456789abc\n\tTotal devices 1 FS bytes used 1\n")); e != nil || uuid != "abcdef12-1234-1234-1234-123456789abc" {
		t.Fatal(uuid, e)
	}
	w := &boundedOutput{remaining: 4}
	if _, e := w.Write([]byte("12345")); e == nil || w.Len() != 0 {
		t.Fatal("unbounded output")
	}
	if !withinDomain("/scope", "/scope/catalog") || withinDomain("/scope", "/scope-other/catalog") {
		t.Fatal("path boundary")
	}
	var nilDomain *BtrfsSnapshotDomain
	if _, e := nilDomain.Qualify("/authority", "/catalog"); e == nil {
		t.Fatal("nil domain")
	}
	for _, scope := range []string{"relative", "/scope/..", "/missing-domain"} {
		if _, e := NewBtrfsSnapshotDomain(scope).Qualify("/authority", "/catalog"); e == nil {
			t.Fatal(scope)
		}
	}
	// No source or mount provisioning, including missing paths and ordinary dirs.
	dir := t.TempDir()
	before, _ := os.ReadDir(dir)
	if _, e := NewBtrfsSnapshotDomain(dir).Qualify(filepath.Join(dir, "authority"), filepath.Join(dir, "catalog")); e == nil {
		t.Fatal("missing directories")
	}
	after, _ := os.ReadDir(dir)
	if len(before) != len(after) {
		t.Fatal("created state")
	}
	f, e := os.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := domainQuery(ctx, f, "inspect-internal", "rootid"); e == nil {
		t.Fatal("cancelled query")
	}
}

// This test never formats or mounts a disk. A guarded external driver must
// supply a fresh Btrfs filesystem on the serial-identified disposable vdb.
func TestBtrfsDomainRootDisposableVM(t *testing.T) {
	if os.Getenv("LYRA_PARENTAL_DOMAIN_VM") != "1" {
		t.Skip("requires disposable Btrfs guest")
	}
	const base = "/root/lyra-go-domain-test"
	const volume = base + "/volume"
	cmdline, _ := os.ReadFile("/proc/cmdline")
	serial, _ := os.ReadFile("/sys/block/vdb/serial")
	mountinfo, _ := os.ReadFile("/proc/self/mountinfo")
	if os.Geteuid() != 0 || !strings.Contains(string(cmdline), "lyra.parental-identity-test=1") || strings.TrimSpace(string(serial)) != "lyra-ref-domain-test" || !strings.Contains(string(mountinfo), " "+volume+" ") || !strings.Contains(string(mountinfo), " - btrfs /dev/vdb ") {
		t.Fatal("disposable disk guard")
	}
	run := func(args ...string) {
		t.Helper()
		out, e := exec.Command("/usr/sbin/btrfs", args...).CombinedOutput()
		if e != nil {
			t.Fatalf("btrfs %v: %v %s", args, e, out)
		}
	}
	scope := volume + "/root"
	authority := volume + "/authority"
	catalog := scope + "/catalog"
	for _, path := range []string{scope, authority} {
		if _, e := os.Lstat(path); !os.IsNotExist(e) {
			t.Fatal("not fresh", path)
		}
		run("subvolume", "create", path)
		if e := os.Chmod(path, 0700); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.Mkdir(catalog, 0700); e != nil {
		t.Fatal(e)
	}
	mount := func(path string) {
		t.Helper()
		out, e := exec.Command("mount", "--bind", path, path).CombinedOutput()
		if e != nil {
			t.Fatal(e, string(out))
		}
	}
	unmount := func(path string) {
		t.Helper()
		out, e := exec.Command("umount", path).CombinedOutput()
		if e != nil {
			t.Fatal(e, string(out))
		}
	}
	mount(scope)
	mount(authority)

	old := decodeReference(t, referenceRow("first-install"))
	current := decodeReference(t, referenceRow("blocked-recovery"))
	writePrivate(t, catalog+"/catalog.lock", nil)
	writePrivate(t, catalog+"/catalog.json", encodeRow(t, referenceRow("first-install")["catalog"].(map[string]any)))
	writePrivate(t, authority+"/reference.lock", nil)
	publish := func(ref RecoveryReference) {
		t.Helper()
		data, e := referenceBytes(ref)
		if e != nil {
			t.Fatal(e)
		}
		writePrivate(t, authority+"/reference.json", data)
		writePrivate(t, authority+"/current.json", encodeRow(t, pinRow(ref)))
	}
	publish(old)
	domain := NewBtrfsSnapshotDomain(scope)
	source := NewFileReferenceSource(authority, catalog, strings.Repeat("f", 32), domain.Qualify)
	witness, e := domain.Qualify(authority, catalog)
	if e != nil {
		t.Fatal("real qualifier", e)
	}
	got, e := source.Snapshot(old.Installation())
	if e != nil || got.Digest() != old.Digest() {
		t.Fatal("root source", e)
	}
	run("subvolume", "snapshot", "-r", scope, volume+"/old-root")
	publish(current)
	writePrivate(t, catalog+"/catalog.json", encodeRow(t, referenceRow("blocked-recovery")["catalog"].(map[string]any)))
	refBefore, _ := os.ReadFile(authority + "/reference.json")
	pinBefore, _ := os.ReadFile(authority + "/current.json")
	unmount(scope)
	if e := os.Rename(scope, volume+"/retired-root"); e != nil {
		t.Fatal(e)
	}
	run("subvolume", "snapshot", volume+"/old-root", scope)
	mount(scope)
	afterWitness, e := domain.Qualify(authority, catalog)
	if e != nil || afterWitness == witness {
		t.Fatal("restored scope identity", e)
	}
	reader := NewReader(catalog)
	local, e := reader.Snapshot()
	if e != nil || local.Digest() != old.Catalog().Digest() {
		t.Fatal("actual rollback", e)
	}
	got, e = source.Snapshot(current.Installation())
	if e != nil || got.Digest() != current.Digest() {
		t.Fatal("current authority survived", e)
	}
	inspector := NewRecoveryInspector(source, reader, func(string) (uint32, error) { return 1002, nil }, func(uint32, string) (bool, error) { return true, nil }, observe)
	assessed, e := inspector.Inspect(":1.2", current.Installation())
	if e != nil || assessed.Disposition() != BlockedRestoreRequired {
		t.Fatal("negative recovery diagnosis", assessed, e)
	}
	refAfter, _ := os.ReadFile(authority + "/reference.json")
	pinAfter, _ := os.ReadFile(authority + "/current.json")
	if !bytes.Equal(refBefore, refAfter) || !bytes.Equal(pinBefore, pinAfter) {
		t.Fatal("reader changed authority")
	}
	if _, e := domain.Qualify(scope, catalog); e == nil {
		t.Fatal("same subvolume")
	}
	run("subvolume", "create", scope+"/nested")
	if e := os.Chmod(scope+"/nested", 0700); e != nil {
		t.Fatal(e)
	}
	if _, e := domain.Qualify(authority, scope+"/nested"); e == nil {
		t.Fatal("catalog outside scope tree")
	}
	run("property", "set", "-t", "subvol", scope, "ro", "true")
	if _, e := domain.Qualify(authority, catalog); e == nil {
		t.Fatal("readonly scope")
	}
	run("property", "set", "-t", "subvol", scope, "ro", "false")
	oldData, _ := referenceBytes(old)
	writePrivate(t, authority+"/reference.json", oldData)
	_, e = source.Snapshot(current.Installation())
	assertCode(t, e, Conflict)
	writePrivate(t, authority+"/reference.json", refBefore)
	if e := os.Remove(authority + "/current.json"); e != nil {
		t.Fatal(e)
	}
	_, e = source.Snapshot(current.Installation())
	assertCode(t, e, RecoveryRequired)
	writePrivate(t, authority+"/current.json", pinBefore)
	run("property", "set", "-t", "subvol", authority, "ro", "true")
	if _, e := domain.Qualify(authority, catalog); e == nil {
		t.Fatal("readonly authority")
	}
	run("property", "set", "-t", "subvol", authority, "ro", "false")
	result := map[string]any{"passed": true, "actual_btrfs_snapshot": true, "catalog_rolled_back": true, "authority_current_preserved": true, "negative_recovery_diagnosis": true, "same_subvolume_refused": true, "nested_catalog_refused": true, "readonly_scope_refused": true, "readonly_authority_refused": true, "old_reference_refused": true, "missing_pin_refused": true, "default_root_source": true, "before_witness": witness, "after_witness": afterWitness, "scope": "single-subvolume-only", "issuer_fixture": true, "production_integrated": false}
	data, e := json.MarshalIndent(result, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(base+"/go-result.json", append(data, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
}
