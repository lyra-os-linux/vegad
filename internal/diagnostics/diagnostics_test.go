package diagnostics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixtures are real captures from an Acer Nitro AN515-57 (Intel + NVIDIA,
// ADATA IM2P33F8 NVMe, Lyra OS 16.1) on 2026-10-29, with the serial number
// and user name replaced.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type call struct {
	out  []byte
	code int
	err  error
}

// fakeEnv answers commands by their joined argv; unknown commands fail the test.
func fakeEnv(t *testing.T, calls map[string]call, files map[string]string, tools ...string) *Env {
	t.Helper()
	return &Env{
		Locale:    "en-US",
		BootsBack: 8,
		Run: func(ctx context.Context, name string, args ...string) ([]byte, int, error) {
			key := strings.Join(append([]string{name}, args...), " ")
			c, ok := calls[key]
			if !ok {
				t.Errorf("unexpected command: %s", key)
				return nil, -1, errors.New("unexpected")
			}
			return c.out, c.code, c.err
		},
		ReadFile: func(path string) ([]byte, error) {
			if content, ok := files[path]; ok {
				return []byte(content), nil
			}
			return nil, os.ErrNotExist
		},
		Glob: func(pattern string) ([]string, error) {
			var matches []string
			for path := range files {
				if ok, _ := filepath.Match(pattern, path); ok {
					matches = append(matches, path)
				}
			}
			return matches, nil
		},
		LookPath: func(name string) (string, error) {
			for _, tool := range tools {
				if tool == name {
					return "/usr/bin/" + name, nil
				}
			}
			return "", errors.New("not found")
		},
	}
}

func noProgress(uint32, string) {}

func findingCodes(findings []Finding) map[string]Finding {
	codes := map[string]Finding{}
	for _, f := range findings {
		codes[f.Code] = f
	}
	return codes
}

const btrfsRoot = "/dev/nvme0n1p2 / btrfs rw,relatime,ssd 0 0\n"

func TestPackagesOnTheWorkstationAreHealthy(t *testing.T) {
	env := fakeEnv(t, map[string]call{
		"rpm -Va":                    {out: fixture(t, "rpm-va.txt"), code: 1},
		"zypper -n verify --dry-run": {out: fixture(t, "zypper-verify.txt")},
	}, nil, "rpm", "zypper")
	result := runPackages(context.Background(), env, noProgress)
	if result.Verdict != Ok {
		t.Fatalf("verdict %s, findings %+v", result.Verdict, result.Findings)
	}
	codes := findingCodes(result.Findings)
	if _, ok := codes["packages.binary-changed"]; ok {
		t.Fatal("mode-only and symlink changes reported as altered binaries")
	}
	config := codes["packages.config-changed"]
	if !strings.Contains(config.Raw, "/etc/default/grub") || !strings.Contains(config.Raw, "/etc/gdm/custom.conf") {
		t.Fatalf("customized configs not listed: %q", config.Raw)
	}
	if strings.Contains(config.Raw, "/etc/os-release") || strings.Contains(codes["packages.metadata-changed"].Raw, "(replaced)") {
		t.Fatal("brand rewrite or replaced files misclassified")
	}
	if result.Metrics["dependenciesSatisfied"] != true {
		t.Fatalf("metrics %+v", result.Metrics)
	}
}

func TestPackagesFlagTamperedBinariesAndBrokenDependencies(t *testing.T) {
	rpm := "S.5....T.    /usr/bin/sudo\nmissing     /usr/lib64/libfoo.so.1\nmissing   d /usr/share/doc/foo/README\n"
	env := fakeEnv(t, map[string]call{
		"rpm -Va": {out: []byte(rpm), code: 1},
		"rpm -qf --qf %{NAME}\n /usr/bin/sudo /usr/lib64/libfoo.so.1": {out: []byte("sudo\nlibfoo1\n")},
		"zypper -n verify --dry-run":                                  {out: []byte("The following NEW package is going to be installed:\n  libbar1\n"), code: 0},
	}, nil, "rpm", "zypper")
	result := runPackages(context.Background(), env, noProgress)
	codes := findingCodes(result.Findings)
	if result.Verdict != Attention || !strings.Contains(codes["packages.binary-changed"].Raw, "/usr/bin/sudo") {
		t.Fatalf("verdict %s, findings %+v", result.Verdict, result.Findings)
	}
	if !strings.Contains(codes["packages.missing"].Raw, "libfoo") || strings.Contains(codes["packages.missing"].Raw, "README") {
		t.Fatalf("missing files: %q", codes["packages.missing"].Raw)
	}
	if _, ok := codes["packages.broken-deps"]; !ok {
		t.Fatal("broken dependencies not reported")
	}
}

func TestDiskHealthOnTheWorkstation(t *testing.T) {
	env := fakeEnv(t, map[string]call{
		"lsblk -J -d -o NAME,TYPE,TRAN": {out: fixture(t, "lsblk.json")},
		"smartctl -a -j /dev/nvme0n1":   {out: fixture(t, "smartctl-nvme.json")},
		"btrfs device stats -c /":       {out: fixture(t, "btrfs-stats.txt")},
	}, map[string]string{"/proc/self/mounts": btrfsRoot}, "smartctl", "btrfs")
	result := runDiskHealth(context.Background(), env, noProgress)
	if result.Verdict != Ok {
		t.Fatalf("verdict %s, findings %+v", result.Verdict, result.Findings)
	}
	codes := findingCodes(result.Findings)
	if poh, ok := codes["smart.poh-unreliable"]; !ok || poh.Severity != Info {
		t.Fatalf("87 h with 23 TB written must be flagged as an unreliable counter: %+v", result.Findings)
	}
	disk := result.Metrics["nvme0n1"].(map[string]any)
	if disk["percentUsed"] != 2 || disk["mediaErrors"] != int64(0) || disk["smartPassed"] != true {
		t.Fatalf("metrics %+v", disk)
	}
	if len(result.Sensitive) != 1 || result.Sensitive[0] != "SERIAL0000TEST" {
		t.Fatalf("serial number not marked sensitive: %v", result.Sensitive)
	}
	if !strings.HasPrefix(result.Summary, "1 of 1") {
		t.Fatalf("zram must not count as a disk: %q", result.Summary)
	}
}

func TestSmartFailuresAndBtrfsCorruption(t *testing.T) {
	smart := strings.Replace(string(fixture(t, "smartctl-nvme.json")), `"critical_warning": 0`, `"critical_warning": 4`, 1)
	smart = strings.Replace(smart, `"media_errors": 0`, `"media_errors": 3`, 1)
	env := fakeEnv(t, map[string]call{
		"lsblk -J -d -o NAME,TYPE,TRAN": {out: fixture(t, "lsblk.json")},
		"smartctl -a -j /dev/nvme0n1":   {out: []byte(smart), code: smartBitErrorLog},
		"btrfs device stats -c /":       {out: []byte("[/dev/nvme0n1p2].corruption_errs  2\n[/dev/nvme0n1p2].read_io_errs 0\n"), code: 64},
	}, map[string]string{"/proc/self/mounts": btrfsRoot}, "smartctl", "btrfs")
	result := runDiskHealth(context.Background(), env, noProgress)
	codes := findingCodes(result.Findings)
	if result.Verdict != Fail {
		t.Fatalf("verdict %s", result.Verdict)
	}
	for _, code := range []string{"smart.critical-warning", "smart.media-errors", "btrfs.device-errors"} {
		if _, ok := codes[code]; !ok {
			t.Errorf("missing %s in %+v", code, result.Findings)
		}
	}
	if _, ok := codes["smart.error-log"]; ok {
		t.Error("the NVMe error log counts host commands and must not raise attention")
	}
	if codes["btrfs.device-errors"].Severity != SeverityFail {
		t.Error("corruption errors must fail")
	}
}

func TestPohHeuristic(t *testing.T) {
	if !pohUnreliable(87, 45072186) {
		t.Error("87 h for 23 TB is implausible")
	}
	if pohUnreliable(5000, 45072186) {
		t.Error("5000 h for 23 TB is plausible")
	}
}

func coredumpJournal() []byte {
	lines := []string{
		`{"MESSAGE_ID":"fc2e22bc6ee647b6b90729ab34a250b1","COREDUMP_EXE":"/usr/libexec/libwebkit2gtk-4_1-0/WebKitWebProcess","COREDUMP_USER_UNIT":"app-gnome-vega-7012.scope","COREDUMP_SIGNAL_NAME":"SIGABRT"}`,
		`{"MESSAGE_ID":"fc2e22bc6ee647b6b90729ab34a250b1","COREDUMP_EXE":"/usr/libexec/libwebkit2gtk-4_1-0/WebKitWebProcess","COREDUMP_USER_UNIT":"app-gnome-vega-9133.scope","COREDUMP_SIGNAL_NAME":"SIGABRT"}`,
		`{"MESSAGE_ID":"fc2e22bc6ee647b6b90729ab34a250b1","COREDUMP_EXE":"/usr/libexec/libwebkit2gtk-4_1-0/WebKitWebProcess","COREDUMP_USER_UNIT":"app-gnome-org.lyraos.Beam-511.scope","COREDUMP_SIGNAL_NAME":"SIGSEGV"}`,
		`{"MESSAGE_ID":"fc2e22bc6ee647b6b90729ab34a250b1","COREDUMP_COMM":"ZoomWebviewHost","COREDUMP_SIGNAL_NAME":"SIGSEGV","COREDUMP_EXE":null}`,
		`{"MESSAGE_ID":"fc2e22bc6ee647b6b90729ab34a250b1","COREDUMP_EXE":"/usr/libexec/libwebkit2gtk-4_1-0/WebKitWebProcess","COREDUMP_USER_UNIT":"vte-spawn-599b4205-023d-4fc7-b8ff-ac901deac42f.scope","COREDUMP_SIGNAL_NAME":"SIGABRT"}`,
		`{"MESSAGE_ID":"fc2e22bc6ee647b6b90729ab34a250b1","COREDUMP_EXE":"/usr/libexec/libwebkit2gtk-4_1-0/WebKitWebProcess","COREDUMP_USER_UNIT":"vte-spawn-711a32cd-b38b-4ff6-b987-4a28d677f1b3.scope","COREDUMP_SIGNAL_NAME":"SIGABRT"}`,
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func logsEnv(t *testing.T, failed string) *Env {
	return fakeEnv(t, map[string]call{
		"journalctl --list-boots -o json --no-pager":                                               {out: []byte(`[{"index":-1},{"index":0}]`)},
		"journalctl -b 0 -p err -o json --no-pager -q":                                             {out: fixture(t, "journal-b0-err.json")},
		"journalctl -b -1 -p err -o json --no-pager -q":                                            {out: fixture(t, "journal-b1-err.json")},
		"journalctl MESSAGE_ID=fc2e22bc6ee647b6b90729ab34a250b1 --since=-7d -o json --no-pager -q": {out: coredumpJournal()},
		"systemctl --failed --output=json --no-pager":                                              {out: []byte(failed)},
	}, nil, "journalctl")
}

func TestLogsClassifyKnownNoiseAndGroupCrashesByApp(t *testing.T) {
	result := runLogs(context.Background(), logsEnv(t, "[]"), noProgress)
	if result.Verdict != Attention {
		t.Fatalf("crashes must raise attention: %s", result.Verdict)
	}
	codes := findingCodes(result.Findings)
	acpi := codes["logs.noise.acpi-firmware"]
	if acpi.Severity != Info || !strings.Contains(acpi.Raw, "AE_AML_REGION_LIMIT") {
		t.Fatalf("ACPI firmware errors not classified as noise: %+v", acpi)
	}
	if !strings.Contains(codes["logs.noise.boot"].Raw, "TDX not supported") {
		t.Fatalf("boot noise: %+v", codes["logs.noise.boot"])
	}
	errorsRaw := codes["logs.errors"].Raw
	if strings.Contains(errorsRaw, "ACPI") || strings.Contains(errorsRaw, "TDX") {
		t.Fatal("known noise leaked into the error list")
	}
	crashes := codes["logs.coredumps"]
	if !strings.Contains(crashes.Raw, "    2  /usr/libexec/libwebkit2gtk-4_1-0/WebKitWebProcess (app-gnome-vega) SIGABRT") ||
		!strings.Contains(crashes.Raw, "(app-gnome-org.lyraos.Beam) SIGSEGV") || !strings.Contains(crashes.Raw, "ZoomWebviewHost") ||
		!strings.Contains(crashes.Raw, "    2  /usr/libexec/libwebkit2gtk-4_1-0/WebKitWebProcess (vte-spawn) SIGABRT") {
		t.Fatalf("coredumps not grouped per app scope: %q", crashes.Raw)
	}
	perBoot := result.Metrics["errorsPerBoot"].(map[string]int)
	if perBoot["0"] != 41 || perBoot["-1"] != 88 {
		t.Fatalf("errors per boot %+v", perBoot)
	}
}

func TestLogsReportFailedUnits(t *testing.T) {
	result := runLogs(context.Background(), logsEnv(t, `[{"unit":"vegad-trusted-keys.service","load":"loaded","active":"failed","sub":"failed","description":"Vega key maintenance"}]`), noProgress)
	units := findingCodes(result.Findings)["logs.failed-units"]
	if units.Severity != SeverityAttention || !strings.Contains(units.Raw, "vegad-trusted-keys.service") {
		t.Fatalf("failed units: %+v", units)
	}
}

func TestJournalStringDecodesByteArrays(t *testing.T) {
	if got := journalString([]byte(`[104,105,255]`)); got != "hi?" {
		t.Fatalf("got %q", got)
	}
	if got := journalString([]byte(`null`)); got != "" {
		t.Fatalf("got %q", got)
	}
}

func platformFiles() map[string]string {
	return map[string]string{
		"/sys/class/hwmon/hwmon5/name":        "coretemp\n",
		"/sys/class/hwmon/hwmon5/temp1_input": "45000\n",
		"/sys/class/hwmon/hwmon5/temp1_label": "Package id 0\n",
		"/sys/class/hwmon/hwmon1/name":        "nvme\n",
		"/sys/class/hwmon/hwmon1/temp1_input": "33850\n",
		"/sys/class/hwmon/hwmon1/temp1_label": "Composite\n",
		"/proc/sys/kernel/tainted":            "4096\n",
		"/sys/fs/selinux/enforce":             "0\n",
		"/sys/power/mem_sleep":                "s2idle [deep]\n",
	}
}

func TestPlatformOnTheWorkstation(t *testing.T) {
	env := fakeEnv(t, map[string]call{
		"upower -e": {out: []byte("/org/freedesktop/UPower/devices/line_power_ACAD\n/org/freedesktop/UPower/devices/battery_BAT1\n")},
		"upower -i /org/freedesktop/UPower/devices/battery_BAT1":                {out: fixture(t, "upower-battery.txt")},
		"fwupdmgr get-updates --json --no-unreported-check --no-metadata-check": {out: []byte(`{"Devices":[]}`)},
		"timedatectl show":   {out: fixture(t, "timedatectl.txt")},
		"mokutil --sb-state": {out: []byte("SecureBoot enabled\n")},
	}, platformFiles(), "upower", "fwupdmgr", "timedatectl", "mokutil")
	result := runPlatform(context.Background(), env, noProgress)
	if result.Verdict != Ok {
		t.Fatalf("verdict %s, findings %+v", result.Verdict, result.Findings)
	}
	m := result.Metrics
	if m["batteryCycles"] != 197 || m["secureBoot"] != true || m["ntpSynchronized"] != true || m["kernelTaint"] != "O" || m["selinux"] != "permissive" {
		t.Fatalf("metrics %+v", m)
	}
	if temps := m["idleTempsC"].(map[string]int); temps["coretemp/Package id 0"] != 45 || temps["nvme/Composite"] != 33 {
		t.Fatalf("temperatures %+v", temps)
	}
	if f := findingCodes(result.Findings)["platform.kernel-tainted-modules"]; f.Severity != Info {
		t.Fatal("an out-of-tree NVIDIA module must be informational")
	}
}

func TestPlatformFlagsFaults(t *testing.T) {
	files := platformFiles()
	files["/sys/class/hwmon/hwmon5/temp1_input"] = "82000\n"
	files["/proc/sys/kernel/tainted"] = "4608\n" // O + W
	env := fakeEnv(t, map[string]call{
		"timedatectl show": {out: []byte("NTP=yes\nNTPSynchronized=no\n")},
	}, files, "timedatectl")
	result := runPlatform(context.Background(), env, noProgress)
	codes := findingCodes(result.Findings)
	for _, code := range []string{"platform.hot-idle", "platform.kernel-tainted", "platform.ntp"} {
		if codes[code].Severity != SeverityAttention {
			t.Errorf("%s: %+v", code, codes[code])
		}
	}
}

func TestCancellationEndsTheStage(t *testing.T) {
	env := fakeEnv(t, map[string]call{"rpm -Va": {err: context.Canceled}}, nil, "rpm")
	if result := runPackages(context.Background(), env, noProgress); result.Verdict != Cancelled {
		t.Fatalf("verdict %s", result.Verdict)
	}
	env = fakeEnv(t, map[string]call{"rpm -Va": {err: context.DeadlineExceeded}}, nil, "rpm")
	if result := runPackages(context.Background(), env, noProgress); result.Verdict != Attention {
		t.Fatalf("timeout verdict %s", result.Verdict)
	}
}

func TestAvailabilityReasons(t *testing.T) {
	env := fakeEnv(t, nil, map[string]string{"/proc/self/mounts": "/dev/sda1 / ext4 rw 0 0\n"}, "rpm", "btrfs")
	cases := map[string]string{
		"packages":    "",
		"disk-health": "missing-tool:smartmontools",
		"fs-scrub":    "not-btrfs",
		"platform":    "",
	}
	for id, want := range cases {
		test, ok := Lookup(id)
		if !ok {
			t.Fatalf("unknown %s", id)
		}
		available, reason := test.Availability(env)
		if reason != want || available != (want == "") {
			t.Errorf("%s: available=%v reason=%q, want %q", id, available, reason, want)
		}
	}
	if mountType(btrfsRoot+"/dev/sda1 /home ext4 rw 0 0\n", "/") != "btrfs" {
		t.Error("root mount type")
	}
}

func TestAggregate(t *testing.T) {
	stage := func(v Verdict) StageResult { return StageResult{Verdict: v} }
	cases := []struct {
		stages []StageResult
		want   Verdict
	}{
		{[]StageResult{stage(Ok), stage(Unavailable)}, Ok},
		{[]StageResult{stage(Ok), stage(Attention)}, Attention},
		{[]StageResult{stage(Fail), stage(Attention)}, Fail},
		{[]StageResult{stage(Fail), stage(Aborted)}, Aborted},
		{[]StageResult{stage(Aborted), stage(Cancelled)}, Cancelled},
	}
	for _, c := range cases {
		if got := Aggregate(c.stages); got != c.want {
			t.Errorf("%v: got %s, want %s", c.stages, got, c.want)
		}
	}
}

func TestEveryTextHasATranslation(t *testing.T) {
	codes := []string{
		"packages.binary-changed", "packages.missing", "packages.config-changed", "packages.metadata-changed", "packages.broken-deps",
		"smart.unreadable", "smart.failed", "smart.prefail", "smart.critical-warning", "smart.spare-low", "smart.worn",
		"smart.media-errors", "smart.error-log", "smart.self-test-errors", "smart.sectors", "smart.hot", "smart.poh-unreliable",
		"smart.no-disks", "btrfs.device-errors", "logs.coredumps", "logs.failed-units", "logs.errors", "logs.noise.acpi-firmware",
		"logs.noise.boot", "logs.noise.gdm-shutdown", "platform.hot-idle", "platform.battery-worn", "platform.firmware-updates",
		"platform.ntp", "platform.ntp-off", "platform.secure-boot-off", "platform.kernel-tainted", "platform.kernel-tainted-modules",
		"stage.timeout", "stage.tool-failed", "thermal.abort", "power.abort", "memory.insufficient", "cpu.verify-failed",
		"memory.verify-failed", "stress.incomplete", "cpu.throttled", "cpu.slow-under-load", "scrub.errors", "scrub.incomplete",
		"bench.insufficient-space", "bench.failed", "bench.io-errors",
		"security.firewall-off", "security.exposed-port", "security.exposed-expected", "security.patches-pending",
		"security.ssh-unreadable", "security.ssh-root-password", "security.ssh-password", "security.selinux",
		"security.secure-boot-off", "security.disk-unencrypted", "security.empty-password", "security.sudo-open",
		"repair.restart-needed",
	}
	for _, locale := range []string{"en-US", "pt-BR", "es-ES"} {
		env := &Env{Locale: locale}
		for _, code := range codes {
			key := "diagnostics.finding." + code
			if text := env.t(key); text == key {
				t.Errorf("%s: no text for %s", locale, key)
			}
		}
	}
}
