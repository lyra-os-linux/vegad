package dbusserver

import (
	"fmt"
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
		"lspci -Dnnd 10de:":     "0000:01:00.0 0300: 10de:1f99 (rev a1) [10de:1f99]",
		"lspci -s 0000:01:00.0": "01:00.0 VGA compatible controller: NVIDIA Corporation TU117M",
		"mokutil --sb-state":    "SecureBoot enabled",
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
	snapshot, err := (nvidiaManager{run: runner}).install(func(_ uint32, message string) { reports = append(reports, message) })
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
