package dbusserver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeNvidiaRunner struct {
	outputs   map[string]string
	errors    map[string]error
	sequences map[string][]fakeNvidiaResponse
	calls     []string
}

type fakeNvidiaResponse struct {
	output string
	err    error
}

func commandKey(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), " ")
}

func (f *fakeNvidiaRunner) Output(name string, args ...string) (string, error) {
	key := commandKey(name, args...)
	f.calls = append(f.calls, key)
	if sequence := f.sequences[key]; len(sequence) > 0 {
		response := sequence[0]
		f.sequences[key] = sequence[1:]
		return response.output, response.err
	}
	return f.outputs[key], f.errors[key]
}

func (f *fakeNvidiaRunner) Run(name string, args ...string) error {
	_, err := f.Output(name, args...)
	return err
}

func compatibleRunner() *fakeNvidiaRunner {
	return &fakeNvidiaRunner{outputs: map[string]string{
		"lspci -Dnd 10de:":                  "0000:01:00.0 0300: 10de:1f99 (rev a1)",
		"lspci -s 0000:01:00.0":             "01:00.0 VGA compatible controller: NVIDIA Corporation TU117M",
		"mokutil --sb-state":                "SecureBoot enabled",
		"rpm -qa --qf %{NAME}|%{VERSION}\n": "bash|5.2",
	}, errors: map[string]error{}, sequences: map[string][]fakeNvidiaResponse{}}
}

func TestSupportedNvidiaDeviceStartsAtTuring(t *testing.T) {
	if supportedNvidiaDevice(0x1c82) {
		t.Fatal("Pascal must not be accepted")
	}
	if !supportedNvidiaDevice(0x1f99) {
		t.Fatal("TU117/Turing must be accepted")
	}
	if !supportedNvidiaDevice(0x25a0) {
		t.Fatal("Ampere must be accepted")
	}
}

func TestNvidiaHardwareParsesNumericLspciOutput(t *testing.T) {
	runner := compatibleRunner()
	name, supported, err := (nvidiaManager{run: runner}).hardware()
	if err != nil {
		t.Fatal(err)
	}
	if !supported || !strings.Contains(name, "TU117M") {
		t.Fatalf("name=%q supported=%v", name, supported)
	}
}

func TestNvidiaStatusRejectsPartialLockstep(t *testing.T) {
	runner := compatibleRunner()
	runner.outputs["rpm -q --qf %{VERSION}-%{RELEASE} "+nvidiaKMPMeta] = "580.1-1"
	runner.errors["rpm -q --qf %{VERSION}-%{RELEASE} "+nvidiaUserspaceMeta] = fmt.Errorf("not installed")
	status, err := (nvidiaManager{run: runner}).status()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "unavailable" || !strings.Contains(status.Detail, "desalinhada") {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestNvidiaInstallCreatesSnapshotAndInstallsMetasTogether(t *testing.T) {
	runner := compatibleRunner()
	runner.outputs["rpm -qa --qf %{NAME}\\n"] = "bash\n"
	runner.outputs["rpm -qa --qf %{NAME}|%{VERSION}\n"] = strings.Join([]string{
		nvidiaKMPMeta + "|580.159.03",
		nvidiaUserspaceMeta + "|580.159.03",
		"nvidia-open-driver-G06-signed-kmp-default|580.159.03_k6.12.0_160000.29",
		"nvidia-video-G06|580.159.03",
	}, "\n")
	runner.outputs["snapper -c root create --type single --read-only --description antes do driver NVIDIA G06 --cleanup-algorithm number --print-number"] = "42"
	runner.errors["zypper --non-interactive lr -u "+nvidiaRepoAlias] = fmt.Errorf("missing")
	for _, packageName := range []string{nvidiaKMPMeta, nvidiaUserspaceMeta} {
		key := "rpm -q --qf %{VERSION}-%{RELEASE} " + packageName
		runner.sequences[key] = []fakeNvidiaResponse{
			{err: fmt.Errorf("not installed")},
			{output: "580.159.03-1"},
		}
	}
	runner.errors["nvidia-smi --query-gpu=name,driver_version --format=csv,noheader"] = fmt.Errorf("not active before reboot")
	var reports []string
	manager := nvidiaManager{run: runner, sleepQuarantinePath: filepath.Join(t.TempDir(), "sleep.conf")}
	snapshot, err := manager.install(func(_ uint32, message string) { reports = append(reports, message) })
	if err != nil {
		t.Fatal(err)
	}
	if snapshot != 42 {
		t.Fatalf("snapshot = %d, want 42", snapshot)
	}
	want := "zypper --non-interactive install --no-recommends " + nvidiaKMPMeta + " " + nvidiaUserspaceMeta
	if !containsString(runner.calls, want) {
		t.Fatalf("lockstep install not called; calls: %v", runner.calls)
	}
	if !containsString(runner.calls, "dracut --force") {
		t.Fatal("dracut was not called")
	}
	if len(reports) == 0 || !strings.Contains(reports[len(reports)-1], "Snapshot de recuperação: 42") {
		t.Fatalf("missing recovery guidance: %v", reports)
	}
}

func TestUpstreamVersionNormalizesRPMReleaseAndKMPKernelSuffix(t *testing.T) {
	for input, want := range map[string]string{
		"580.159.03-lp160.53.1":        "580.159.03",
		"580.159.03_k6.12.0_160000.29": "580.159.03",
		"580.173.02":                   "580.173.02",
	} {
		if got := upstreamVersion(input); got != want {
			t.Fatalf("upstreamVersion(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestNvidiaStatusRejectsMisalignedLeafPackages(t *testing.T) {
	runner := compatibleRunner()
	runner.outputs["rpm -q --qf %{VERSION}-%{RELEASE} "+nvidiaKMPMeta] = "580.159.03-1"
	runner.outputs["rpm -q --qf %{VERSION}-%{RELEASE} "+nvidiaUserspaceMeta] = "580.159.03-1"
	runner.outputs["rpm -qa --qf %{NAME}|%{VERSION}\n"] = "nvidia-video-G06|580.159.03\nnvidia-gl-G06|580.142"

	status, err := (nvidiaManager{run: runner}).status()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "unavailable" || !strings.Contains(status.Detail, "580.142, 580.159.03") {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestNvidiaSuspendPolicyQuarantinesKnownBadHybridVersion(t *testing.T) {
	runner := compatibleRunner()
	runner.outputs["lspci -Dnd ::0300"] = "0000:00:02.0 0300: 8086:9a68\n0000:01:00.0 0300: 10de:25a0"
	path := filepath.Join(t.TempDir(), "sleep.conf")
	manager := nvidiaManager{run: runner, sleepQuarantinePath: path}

	quarantined, err := manager.reconcileSuspendPolicy("580.159.03-lp160.53.1")
	if err != nil {
		t.Fatal(err)
	}
	if !quarantined {
		t.Fatal("known-bad hybrid combination was not quarantined")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{nvidiaSleepQuarantineMarker, "AllowSuspend=no", "AllowHibernation=no"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("quarantine is missing %q: %s", expected, text)
		}
	}
	if !containsString(runner.calls, "systemctl daemon-reload") {
		t.Fatalf("systemd was not reloaded: %v", runner.calls)
	}
}

func TestNvidiaSuspendPolicyRemovesOnlyManagedQuarantine(t *testing.T) {
	runner := compatibleRunner()
	path := filepath.Join(t.TempDir(), "sleep.conf")
	if err := os.WriteFile(path, []byte(nvidiaSleepQuarantineMarker+"\n[Sleep]\nAllowSuspend=no\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := nvidiaManager{run: runner, sleepQuarantinePath: path}

	quarantined, err := manager.reconcileSuspendPolicy("580.173.02-1")
	if err != nil {
		t.Fatal(err)
	}
	if quarantined {
		t.Fatal("qualified version remained quarantined")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed quarantine was not removed: %v", err)
	}
}

func TestNvidiaSuspendPolicyPreservesAdministratorDropIn(t *testing.T) {
	runner := compatibleRunner()
	path := filepath.Join(t.TempDir(), "sleep.conf")
	contents := "# configuração do administrador\n[Sleep]\nAllowSuspend=no\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := nvidiaManager{run: runner, sleepQuarantinePath: path}

	if _, err := manager.reconcileSuspendPolicy("580.173.02-1"); err == nil || !strings.Contains(err.Error(), "não é gerenciado pelo Vega") {
		t.Fatalf("administrator drop-in was not protected: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != contents {
		t.Fatalf("administrator drop-in changed: %q", got)
	}
}

func TestNvidiaQualificationRejectsAffectedHybridVersion(t *testing.T) {
	runner := compatibleRunner()
	runner.outputs["lspci -Dnd ::0300"] = "0000:00:02.0 0300: 8086:9a68\n0000:01:00.0 0300: 10de:25a0"
	qualified, reason := (nvidiaManager{run: runner}).suspendQualified("580.159.03-1")
	if qualified || !strings.Contains(reason, "não está qualificada") {
		t.Fatalf("unexpected qualification result: qualified=%v reason=%q", qualified, reason)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
