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
	for name, want := range map[string]journalFile{
		"system@b41b1382471d42ddb074a71590700311-00000000000a2c1f-00065e814d0d02ae.journal": {prefix: "system", head: 0x65e814d0d02ae, seqID: "b41b1382471d42ddb074a71590700311", seq: 0xa2c1f},
		"user-1001@00065e814d0d02ae-7a3b9c1d2e4f5a6b.journal~":                              {prefix: "user-1001", head: 0x65e814d0d02ae},
	} {
		file, ok := parseArchivedName(name)
		if !ok || file != want {
			t.Fatalf("%s: %+v %v", name, file, ok)
		}
	}
	for _, name := range []string{"system.journal", "user-1001.journal", "system@x.journal", "notes.txt",
		"system@b41b-0000000000000001-00065e814d0d02ae.journal"} {
		if _, ok := parseArchivedName(name); ok {
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
	if plan := journalCandidates(files, 1100<<20, bootCutoff{realtime: previousBoot - 11}); len(plan) != 0 {
		t.Fatalf("%+v", plan)
	}
	if plan := journalCandidates(files, 1100<<20, bootCutoff{realtime: previousBoot + 11}); len(plan) != 1 || plan[0].head != previousBoot-10 {
		t.Fatalf("%+v", plan)
	}
}

// vegad#126: the previous boot began with the clock a day ahead, so its
// files carry later times than the current boot's. Seqnums still say which
// file ends before the previous boot; the clock must not.
func TestJournalSeqnumsWinOverAClockThatRanAhead(t *testing.T) {
	const id = "b41b1382471d42ddb074a71590700311"
	day := uint64(24 * time.Hour / time.Microsecond)
	files := []journalFile{
		{prefix: "m/system", seqID: id, seq: 100, head: previousBoot - 3*day, size: 400 << 20},
		{prefix: "m/system", seqID: id, seq: 200, head: previousBoot - 2*day, size: 400 << 20},
		// Previous boot (seqnum 250 onwards), written with the clock ahead.
		{prefix: "m/system", seqID: id, seq: 300, head: previousBoot + 5*day, size: 400 << 20},
		// Current boot, after NTP set the clock back.
		{prefix: "m/system", seqID: id, seq: 400, head: previousBoot + day, size: 400 << 20},
	}
	cut := bootCutoff{realtime: previousBoot + 5*day - 10, seqID: id, seq: 250}
	plan := journalCandidates(files, 1600<<20, cut)
	if len(plan) != 1 || plan[0].seq != 100 {
		t.Fatalf("only the file that ends before seqnum 250 may go: %+v", plan)
	}
	// Without the seqnum the same clock would let a previous-boot file go.
	if plan := journalCandidates(files, 1600<<20, bootCutoff{realtime: cut.realtime}); len(plan) < 2 {
		t.Fatalf("realtime fallback changed: %+v", plan)
	}
}

func TestJournalAsksForTheSeqnumOfThePreviousBoot(t *testing.T) {
	h := newHost(t)
	h.replies["journalctl --list-boots -o json --no-pager"] = reply{out: fixture(t, "list-boots.json")}
	h.replies["journalctl -b -1 -o json --no-pager --lines=+1 --output-fields=__SEQNUM,__SEQNUM_ID"] = reply{
		out: `{"__CURSOR":"s=541c90f0;i=9537","__SEQNUM":"38199","__SEQNUM_ID":"541C90F0536241BCBC79E18166D587CE"}` + "\n"}
	cut, err := previousBootStart(context.Background(), h.env)
	if err != nil || cut.realtime != previousBoot || cut.seq != 38199 || cut.seqID != "541c90f0536241bcbc79e18166d587ce" {
		t.Fatalf("%+v %v", cut, err)
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

// vega#242: what the runtime shares (by hard link) with runtimes that stay is
// not freed, so it does not count; unrecognized output is not "nothing".
func TestFlatpakUnusedCountsOnlyWhatIsNotShared(t *testing.T) {
	h := newHost(t)
	h.replies["flatpak uninstall --unused --system"] = reply{out: fixture(t, "flatpak-unused.txt"), code: 1}
	own := h.write(flatpakSystemDir+"/repo/objects/own", 64<<10)
	shared := h.write(flatpakSystemDir+"/repo/objects/shared", 256<<10)
	gone := h.env.path(flatpakSystemDir + "/runtime/org.gtk.Gtk3theme.Adwaita-dark/x86_64/3.22/active/files")
	stays := h.env.path(flatpakSystemDir + "/runtime/org.gnome.Platform/x86_64/49/active/files")
	for _, dir := range []string{gone, stays} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, link := range [][2]string{{own, gone + "/own"}, {shared, gone + "/shared"}, {shared, stays + "/shared"}} {
		if err := os.Link(link[0], link[1]); err != nil {
			t.Fatal(err)
		}
	}
	m, err := measureFlatpak(context.Background(), h.env)
	if err != nil {
		t.Fatal(err)
	}
	if m.bytes < 64<<10 || m.bytes >= 256<<10 {
		t.Fatalf("só o exclusivo deveria contar: %d", m.bytes)
	}

	h.replies["flatpak uninstall --unused --system"] = reply{out: "error: something new\n", code: 1}
	if _, err := measureFlatpak(context.Background(), h.env); err == nil {
		t.Fatal("saída não reconhecida não pode virar \"nada a limpar\"")
	}
}

// vegad#127: Clean of flatpak-unused runs the non-interactive uninstall and
// reports what the measurement found as freed.
func TestFlatpakUnusedCleanUninstallsAndReportsFreedBytes(t *testing.T) {
	h := newHost(t)
	runtime := h.write(flatpakSystemDir+"/runtime/org.gtk.Gtk3theme.Adwaita-dark/x86_64/3.22/active/files/gtk.css", 12<<10)
	h.replies["flatpak uninstall --unused --system"] = reply{out: fixture(t, "flatpak-unused.txt"), code: 1}
	h.replies["flatpak uninstall --unused --system --noninteractive -y"] = reply{effect: func() {
		_ = os.RemoveAll(h.env.path(flatpakSystemDir + "/runtime"))
		h.replies["flatpak uninstall --unused --system"] = reply{out: fixture(t, "flatpak-unused-nothing.txt")}
	}}
	freed, result, err := Clean(context.Background(), h.env, "flatpak-unused")
	if err != nil || result != ResultOK || freed < 12<<10 {
		t.Fatalf("freed %d, result %s, err %v", freed, result, err)
	}
	if !h.ran("flatpak uninstall --unused --system --noninteractive -y") {
		t.Fatal("the unused runtimes were not uninstalled")
	}
	if _, err := os.Stat(runtime); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime still on disk: %v", err)
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

// vegad#124: with Snapper (the Desktop) the purge's snapshots keep the
// kernel files, so Scan says "deferred" and Clean reports 0 freed bytes.
func TestKernelsWithSnapperAreDeferredAndFreeNothingYet(t *testing.T) {
	old := strings.Replace(fixture(t, "purge-kernels-remove.txt"),
		"kernel-default-6.12.0-160100.8.1 kernel-default-extra kernel-default-optional", "kernel-default-6.12.0-160100.5.1", 1)
	h := kernelHost(t, old)
	h.tools["snapper"] = true
	h.replies["snapper --machine-readable csv get-config"] = reply{out: fixture(t, "snapper-config.csv")}
	m, err := measureKernels(context.Background(), h.env)
	if err != nil || !m.deferred || !m.cleanable() {
		t.Fatalf("%+v %v", m, err)
	}
	if detail := m.category("old-kernels").Detail; !strings.Contains(detail, "deferred") {
		t.Fatalf("detail %q", detail)
	}
	h.replies["zypper -n --no-refresh purge-kernels"] = reply{}
	freed, result, err := Clean(context.Background(), h.env, "old-kernels")
	if err != nil || result != ResultOK || freed != 0 || !h.ran("zypper -n --no-refresh purge-kernels") {
		t.Fatalf("freed=%d result=%s err=%v", freed, result, err)
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
