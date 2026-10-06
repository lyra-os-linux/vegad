package parental

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestSnapperDomainRootDisposableVM(t *testing.T) {
	if os.Getenv("LYRA_PARENTAL_SNAPPER_VM") != "1" {
		t.Skip("requires disposable Btrfs guest")
	}
	const base = "/root/lyra-go-snapper-test"
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
	config := "/etc/snapper/configs/lyra-parental-lab"
	if _, e := os.Lstat(config); !os.IsNotExist(e) {
		t.Fatal("existing lab config")
	}
	writePrivate(t, config, snapperTestConfig(scope))
	defer os.Remove(config)
	run("subvolume", "create", scope+"/.snapshots")
	if e := os.Chmod(scope+"/.snapshots", 0700); e != nil {
		t.Fatal(e)
	}
	domain := NewSnapperSnapshotDomain(config, scope)
	source := NewFileReferenceSource(authority, catalog, strings.Repeat("f", 32), domain.Qualify)
	witness, e := domain.Qualify(authority, catalog)
	if e != nil {
		t.Fatal("real qualifier", e)
	}
	got, e := source.Snapshot(old.Installation())
	if e != nil || got.Digest() != old.Digest() {
		t.Fatal("root source", e)
	}
	out, e := exec.Command("/usr/bin/snapper", "--no-dbus", "--config", "lyra-parental-lab", "create", "--print-number", "--description", "parental-scope-lab").CombinedOutput()
	if e != nil {
		t.Fatal("Snapper create", e, string(out))
	}
	number := strings.TrimSpace(string(out))
	n, e := strconv.ParseUint(number, 10, 63)
	if e != nil || n < 1 || strconv.FormatUint(n, 10) != number {
		t.Fatal("snapshot number", number, e)
	}
	snapshot := scope + "/.snapshots/" + fmt.Sprint(n) + "/snapshot"
	savedData, e := os.ReadFile(snapshot + "/catalog/catalog.json")
	if e != nil {
		t.Fatal(e)
	}
	originalData, _ := os.ReadFile(catalog + "/catalog.json")
	if !bytes.Equal(savedData, originalData) {
		t.Fatal("Snapper did not capture catalog")
	}
	run("subvolume", "snapshot", "-r", snapshot, volume+"/old-root")
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
	// Changes to the bound configuration refuse or change the witness.
	initialConfig, _ := os.ReadFile(config)
	writePrivate(t, config, bytes.ReplaceAll(initialConfig, []byte("ALLOW_USERS=\"\""), []byte("ALLOW_USERS=\"child\"")))
	if _, e := domain.Qualify(authority, catalog); e == nil {
		t.Fatal("delegated Snapper access")
	}
	writePrivate(t, config, initialConfig)
	qualified, e := domain.Qualify(authority, catalog)
	if e != nil {
		t.Fatal(e)
	}
	writePrivate(t, config, append(initialConfig, []byte("# maintenance changed\n")...))
	changed, e := domain.Qualify(authority, catalog)
	if e != nil || changed == qualified {
		t.Fatal("config witness not bound", e)
	}
	writePrivate(t, config, initialConfig)

	result := map[string]any{"passed": true, "actual_btrfs_snapshot": true, "snapper_create": true, "snapper_config_bound": true, "boot_rollback_qualified": false, "catalog_rolled_back": true, "authority_current_preserved": true, "negative_recovery_diagnosis": true, "same_subvolume_refused": true, "nested_catalog_refused": true, "readonly_scope_refused": true, "readonly_authority_refused": true, "old_reference_refused": true, "missing_pin_refused": true, "default_root_source": true, "before_witness": witness, "after_witness": afterWitness, "scope": "single-subvolume-only", "issuer_fixture": true, "production_integrated": false}
	data, e := json.MarshalIndent(result, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(base+"/go-result.json", append(data, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
}
