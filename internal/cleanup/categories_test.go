package cleanup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const machine = "/var/log/journal/0123456789abcdef0123456789abcdef"

// Boots of the real fixture: the previous one starts at 1793211933587970.
const previousBoot = uint64(1793211933587970)

func archived(prefix string, head uint64) string {
	return fmt.Sprintf("%s/%s@b41b1382471d42ddb074a71590700311-%016x-%016x.journal", machine, prefix, 1, head)
}

func TestArchivedJournalNamesCarryTheHeadTime(t *testing.T) {
	for name, want := range map[string]uint64{
		"system@b41b1382471d42ddb074a71590700311-0000000000000001-00065e814d0d02ae.journal": 0x65e814d0d02ae,
		"user-1001@00065e814d0d02ae-7a3b9c1d2e4f5a6b.journal~":                              0x65e814d0d02ae,
	} {
		_, head, ok := parseArchivedName(name)
		if !ok || head != want {
			t.Fatalf("%s: %x %v", name, head, ok)
		}
	}
	for _, name := range []string{"system.journal", "user-1001.journal", "system@x.journal", "notes.txt"} {
		if _, _, ok := parseArchivedName(name); ok {
			t.Fatalf("%s aceito", name)
		}
	}
}

// smallJournal scales the limit to 500 KiB so the files stay small.
func smallJournal(t *testing.T) {
	saved := journalLimit
	journalLimit = 500 << 10
	t.Cleanup(func() { journalLimit = saved })
}

func TestJournalUnderTheLimitHasNothingToClean(t *testing.T) {
	smallJournal(t)
	h := newHost(t)
	h.write(machine+"/system.journal", 16<<10)
	h.write(archived("system", previousBoot-1e12), 56<<10)
	m, err := measureJournal(context.Background(), h.env)
	if err != nil || m.cleanable() {
		t.Fatalf("%+v %v", m, err)
	}
	if h.ran("journalctl --list-boots -o json --no-pager") {
		t.Fatal("abaixo do limite não precisa listar boots")
	}
}

func TestJournalKeepsThePreviousBootAndStopsAtTheLimit(t *testing.T) {
	smallJournal(t)
	h := newHost(t)
	h.replies["journalctl --list-boots -o json --no-pager"] = reply{out: fixture(t, "list-boots.json")}
	h.write(machine+"/system.journal", 16<<10)
	day := uint64(24 * time.Hour / time.Microsecond)
	// Five archived files of 128 KiB; the fifth starts inside the previous
	// boot, so the fourth holds entries of it too.
	var paths []string
	heads := []uint64{previousBoot - 4*day, previousBoot - 3*day, previousBoot - 2*day, previousBoot + 10, previousBoot + day}
	for _, head := range heads {
		paths = append(paths, h.write(archived("system", head), 128<<10))
	}
	m, err := measureJournal(context.Background(), h.env)
	if err != nil {
		t.Fatal(err)
	}
	// Total 656 KiB; removing the oldest two reaches 400 KiB ≤ 500 KiB.
	if m.count != 2 || m.bytes < 256<<10 {
		t.Fatalf("%+v", m)
	}
	if _, _, err := Clean(context.Background(), h.env, "journal"); err != nil {
		t.Fatal(err)
	}
	for i, path := range paths {
		_, err := os.Stat(path)
		if gone := errors.Is(err, os.ErrNotExist); gone != (i < 2) {
			t.Fatalf("arquivo %d: removido=%v", i, gone)
		}
	}
}

func TestJournalNeverRemovesFilesThatReachThePreviousBoot(t *testing.T) {
	files := []journalFile{
		{prefix: "m/system", head: previousBoot - 10, size: 400 << 20},
		{prefix: "m/system", head: previousBoot + 10, size: 400 << 20},
		{prefix: "m/user-1001", head: previousBoot - 20, size: 300 << 20},
	}
	// The first system file ends after the previous boot began, and the only
	// user file has no successor: nothing is provably older than that boot.
	if plan := journalCandidates(files, 1100<<20, previousBoot-11); len(plan) != 0 {
		t.Fatalf("%+v", plan)
	}
	if plan := journalCandidates(files, 1100<<20, previousBoot+11); len(plan) != 1 || plan[0].head != previousBoot-10 {
		t.Fatalf("%+v", plan)
	}
}

func TestFlatpakUnusedComesFromFlatpakAndAnswersNo(t *testing.T) {
	h := newHost(t)
	h.replies["flatpak uninstall --unused --system"] = reply{out: fixture(t, "flatpak-unused.txt"), code: 1}
	h.write(flatpakSystemDir+"/runtime/org.gtk.Gtk3theme.Adwaita-dark/x86_64/3.22/active/files/gtk.css", 12<<10)
	m, err := measureFlatpak(context.Background(), h.env)
	if err != nil {
		t.Fatal(err)
	}
	if m.count != 1 || m.bytes < 12<<10 || m.items[0] != "org.gtk.Gtk3theme.Adwaita-dark//3.22" {
		t.Fatalf("%+v", m)
	}
	if h.calls[0].stdin != "n\n" {
		t.Fatalf("a listagem precisa recusar: %q", h.calls[0].stdin)
	}

	h.replies["flatpak uninstall --unused --system"] = reply{out: fixture(t, "flatpak-unused-nothing.txt")}
	if m, _ := measureFlatpak(context.Background(), h.env); m.cleanable() {
		t.Fatalf("%+v", m)
	}
}

func TestPurgeKernelsDryRunParsesTheRealOutput(t *testing.T) {
	plan, err := parsePurgeKernels([]byte(fixture(t, "purge-kernels-remove.txt")))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"kernel-default-6.12.0-160100.8.1", "kernel-default-extra", "kernel-default-optional"}
	if strings.Join(plan.packages, " ") != strings.Join(want, " ") {
		t.Fatalf("%v", plan.packages)
	}
	released := 177.7
	if plan.bytes != uint64(released*(1<<20)) {
		t.Fatalf("%d", plan.bytes)
	}
	nothing, err := parsePurgeKernels([]byte(fixture(t, "purge-kernels-nothing.txt")))
	if err != nil || len(nothing.packages) != 0 || nothing.bytes != 0 {
		t.Fatalf("%+v %v", nothing, err)
	}
}

func kernelHost(t *testing.T, dryRun string) *fakeHost {
	h := newHost(t)
	h.write("/proc/sys/kernel/osrelease", 0)
	if err := os.WriteFile(h.env.path("/proc/sys/kernel/osrelease"), []byte("6.12.0-160100.7-default\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.replies["zypper -n --no-refresh purge-kernels --dry-run"] = reply{out: dryRun}
	h.replies[`rpm -q --qf %{VERSION}-%{RELEASE}\n kernel-default`] = reply{out: "6.12.0-160100.5.1\n6.12.0-160100.7.2\n6.12.0-160100.8.1\n"}
	h.replies[`rpm -q --qf %{VERSION}-%{RELEASE}\n kernel-default-extra`] = reply{out: "6.12.0-160100.8.1\n"}
	h.replies[`rpm -q --qf %{VERSION}-%{RELEASE}\n kernel-default-optional`] = reply{out: "6.12.0-160100.8.1\n"}
	return h
}

func TestKernelsRefuseAPlanWithTheNewestOrRunningKernel(t *testing.T) {
	// The real dry-run with "multiversion.kernels = running" would take the
	// newest kernel: refused whatever the configuration says.
	h := kernelHost(t, fixture(t, "purge-kernels-remove.txt"))
	m, err := measureKernels(context.Background(), h.env)
	if err != nil || m.cleanable() || m.reason != "protected" {
		t.Fatalf("%+v %v", m, err)
	}
	if _, result, _ := Clean(context.Background(), h.env, "old-kernels"); result != ResultSkipped || h.ran("zypper -n --no-refresh purge-kernels") {
		t.Fatal("plano protegido executado")
	}

	running := strings.Replace(fixture(t, "purge-kernels-remove.txt"),
		"kernel-default-6.12.0-160100.8.1 kernel-default-extra kernel-default-optional", "kernel-default-6.12.0-160100.7.2", 1)
	h = kernelHost(t, running)
	if m, _ := measureKernels(context.Background(), h.env); m.reason != "protected" {
		t.Fatalf("kernel em uso: %+v", m)
	}
}

func TestKernelsCleanAnOldKernelInsideASnapshot(t *testing.T) {
	old := strings.Replace(fixture(t, "purge-kernels-remove.txt"),
		"kernel-default-6.12.0-160100.8.1 kernel-default-extra kernel-default-optional", "kernel-default-6.12.0-160100.5.1", 1)
	h := kernelHost(t, old)
	m, err := measureKernels(context.Background(), h.env)
	if err != nil || !m.cleanable() || m.count != 1 {
		t.Fatalf("%+v %v", m, err)
	}
	h.replies["zypper -n --no-refresh purge-kernels"] = reply{effect: func() {
		h.replies["zypper -n --no-refresh purge-kernels --dry-run"] = reply{out: fixture(t, "purge-kernels-nothing.txt")}
	}}
	freed, result, err := Clean(context.Background(), h.env, "old-kernels")
	if err != nil || result != ResultOK || freed != m.bytes || h.snaps != 1 {
		t.Fatalf("freed=%d result=%s err=%v snaps=%d", freed, result, err, h.snaps)
	}
}

func TestKernelsReportBusyWhenZypperIsLocked(t *testing.T) {
	h := kernelHost(t, "")
	h.replies["zypper -n --no-refresh purge-kernels --dry-run"] = reply{code: 7}
	if m, _ := measureKernels(context.Background(), h.env); m.reason != "busy" {
		t.Fatalf("%+v", m)
	}
}

func TestVersionsCompareNumerically(t *testing.T) {
	if compareVersions("6.12.0-160100.10.1", "6.12.0-160100.9.1") <= 0 ||
		compareVersions("6.12.0-160100.8.1", "6.12.0-160100.8.1") != 0 ||
		compareVersions("6.12.0-160100.7.2", "6.12.0-160100.8.1") >= 0 {
		t.Fatal("ordem de versões")
	}
}

func snapperHost(t *testing.T, list string) *fakeHost {
	h := newHost(t)
	h.replies["snapper --machine-readable csv get-config"] = reply{out: fixture(t, "snapper-config.csv")}
	h.replies["snapper --iso --machine-readable csv list --columns number,type,date,cleanup,userdata"] = reply{out: list}
	return h
}

func TestSnapshotsWithinThePolicyHaveNothingToClean(t *testing.T) {
	h := snapperHost(t, fixture(t, "snapper-list.csv"))
	m, err := measureSnapshots(context.Background(), h.env)
	if err != nil || m.cleanable() || !m.sizeUnknown || !m.minimum {
		t.Fatalf("%+v %v", m, err)
	}
}

func TestSnapshotsAboveTheLimitCountOnlyOldEnoughOnes(t *testing.T) {
	var list strings.Builder
	list.WriteString("number,type,date,cleanup,userdata\n0,single,,,\n")
	start := time.Date(2026, 9, 1, 10, 0, 0, 0, time.Local)
	for i := 1; i <= 54; i++ {
		date := start.Add(time.Duration(i) * time.Hour)
		if i > 52 {
			date = time.Date(2026, 10, 29, 20, 30, 0, 0, time.Local) // younger than NUMBER_MIN_AGE
		}
		fmt.Fprintf(&list, "%d,single,%s,number,\n", i, date.Format("2006-01-02 15:04:05"))
	}
	h := snapperHost(t, list.String())
	m, err := measureSnapshots(context.Background(), h.env)
	// 54 number snapshots, limit 50: at least 4 go, and all four are old.
	if err != nil || m.count != 4 || !m.cleanable() {
		t.Fatalf("%+v %v", m, err)
	}
	h.replies["snapper cleanup number"] = reply{}
	h.replies["snapper cleanup timeline"] = reply{}
	if _, result, err := Clean(context.Background(), h.env, "snapshots"); err != nil || result != ResultOK {
		t.Fatalf("%s %v", result, err)
	}
	if !h.ran("snapper cleanup number") || !h.ran("snapper cleanup timeline") {
		t.Fatalf("%v", h.calls)
	}
}

func TestSnapshotsWithoutConfigAreNotConfigured(t *testing.T) {
	h := newHost(t)
	h.replies["snapper --machine-readable csv get-config"] = reply{code: 1}
	if m, _ := measureSnapshots(context.Background(), h.env); m.reason != "not-configured" {
		t.Fatalf("%+v", m)
	}
}
