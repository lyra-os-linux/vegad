package dbusserver

import (
	"errors"
	"github.com/lyraos/vegad/internal/profile"
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
		"uname -m":                          "x86_64",
		"uname -r":                          "6.12.0-160100.7-default",
		"readlink -f /boot/vmlinuz":         "/usr/lib/modules/6.12.0-160100.7-default/vmlinuz",
		"rpm -qa --qf %{NAME}|%{VERSION}\n": "bash|5.2\nLeap-release|16.1",
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

func TestUpstreamVersionNormalizesRPMReleaseAndKMPKernelSuffix(t *testing.T) {
	for input, want := range map[string]string{
		"580.159.03-lp160.53.1":        "580.159.03",
		"580.159.03_k6.12.0_160000.29": "580.159.03",
		"580.173.02":                   "580.173.02",
		"615.71.09_k6.12.0_160100.7":   "615.71.09",
	} {
		if got := upstreamVersion(input); got != want {
			t.Fatalf("upstreamVersion(%q)=%q, want %q", input, got, want)
		}
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

func officialManager(t *testing.T) (nvidiaManager, *fakeNvidiaRunner) {
	t.Helper()
	r := compatibleRunner()
	inventory := "Leap-release|16.1\nlyra-nvidia|" + nvidiaIntegrationVersion
	for _, pkg := range nvidiaRequiredPackages {
		inventory += "\n" + pkg + "|" + nvidiaIntegrationVersion
	}
	r.outputs["rpm -qa --qf %{NAME}|%{VERSION}\n"] = inventory
	kernel := "6.12.0-160100.7-default"
	module := "/usr/lib/modules/" + kernel + "/updates/nvidia.ko.zst"
	r.outputs["modinfo -k "+kernel+" -F version nvidia"] = nvidiaIntegrationVersion
	r.outputs["modinfo -k "+kernel+" -F signer nvidia"] = "SUSE Linux Enterprise Secure Boot CA"
	r.outputs["modinfo -k "+kernel+" -F filename nvidia"] = module
	r.outputs["readlink -f "+module] = module
	r.outputs["rpm -qf --qf %{NAME} "+module] = nvidiaSignedKMP
	r.outputs["nvidia-smi --query-gpu=pci.bus_id,driver_version --format=csv,noheader"] = "00000000:01:00.0, " + nvidiaIntegrationVersion
	m := nvidiaManager{run: r, sysRoot: t.TempDir(), sleepQuarantinePath: filepath.Join(t.TempDir(), "sleep.conf"), recoveryPath: filepath.Join(t.TempDir(), "recovery")}
	for path, data := range map[string]string{"module/nvidia/version": nvidiaIntegrationVersion, "bus/pci/drivers/nvidia/.placeholder": ""} {
		dest := m.sysPath(path)
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	dest := m.sysPath("bus/pci/devices/0000:01:00.0/driver")
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(m.sysPath("bus/pci/drivers/nvidia"), dest); err != nil {
		t.Fatal(err)
	}
	return m, r
}

func TestOfficialNvidiaStatus(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		change      func(nvidiaManager, *fakeNvidiaRunner)
	}{
		{"healthy", "active", func(m nvidiaManager, r *fakeNvidiaRunner) {}},
		{"missing-guard", "unmanaged", func(m nvidiaManager, r *fakeNvidiaRunner) {
			r.outputs["rpm -qa --qf %{NAME}|%{VERSION}\n"] = strings.ReplaceAll(r.outputs["rpm -qa --qf %{NAME}|%{VERSION}\n"], "lyra-nvidia|615.71.09", "")
		}},
		{"legacy", "conflict", func(m nvidiaManager, r *fakeNvidiaRunner) {
			r.outputs["rpm -qa --qf %{NAME}|%{VERSION}\n"] += "\nnvidia-gl-G06|580.159.03"
		}},
		{"dkms", "conflict", func(m nvidiaManager, r *fakeNvidiaRunner) {
			r.outputs["rpm -qa --qf %{NAME}|%{VERSION}\n"] += "\nnvidia-open-driver-G07|615.71.09"
		}},
		{"newer-library", "inconsistent", func(m nvidiaManager, r *fakeNvidiaRunner) {
			key := "rpm -qa --qf %{NAME}|%{VERSION}\n"
			r.outputs[key] = strings.ReplaceAll(r.outputs[key], "nvidia-gl-G07|615.71.09", "nvidia-gl-G07|620.10.01")
		}},
		{"missing-library", "inconsistent", func(m nvidiaManager, r *fakeNvidiaRunner) {
			key := "rpm -qa --qf %{NAME}|%{VERSION}\n"
			r.outputs[key] = strings.ReplaceAll(r.outputs[key], "nvidia-gl-G07|615.71.09", "")
		}},
		{"independent-egl", "active", func(m nvidiaManager, r *fakeNvidiaRunner) {
			r.outputs["rpm -qa --qf %{NAME}|%{VERSION}\n"] += "\nlibnvidia-egl-gbm1|1.1.3"
		}},
		{"nvml-failed", "driver-error", func(m nvidiaManager, r *fakeNvidiaRunner) {
			r.errors["nvidia-smi --query-gpu=pci.bus_id,driver_version --format=csv,noheader"] = errors.New("mismatch")
		}},
		{"loaded-old", "reboot-required", func(m nvidiaManager, r *fakeNvidiaRunner) {
			if err := os.WriteFile(m.sysPath("module/nvidia/version"), []byte("595.1"), 0644); err != nil {
				t.Fatal(err)
			}
		}},
		{"kernel-module-missing", "kernel-missing", func(m nvidiaManager, r *fakeNvidiaRunner) {
			r.outputs["modinfo -k 6.12.0-160100.7-default -F version nvidia"] = ""
		}},
		{"next-kernel-missing", "kernel-missing", func(m nvidiaManager, r *fakeNvidiaRunner) {
			r.outputs["readlink -f /boot/vmlinuz"] = "/boot/vmlinuz-6.12.0-160100.8-default"
		}},
		{"unsigned", "unsigned-module", func(m nvidiaManager, r *fakeNvidiaRunner) {
			r.outputs["modinfo -k 6.12.0-160100.7-default -F signer nvidia"] = "Administrator key"
		}},
		{"wrong-owner", "unsigned-module", func(m nvidiaManager, r *fakeNvidiaRunner) {
			r.outputs["rpm -qf --qf %{NAME} /usr/lib/modules/6.12.0-160100.7-default/updates/nvidia.ko.zst"] = "nvidia-open-driver-G07"
		}},
		{"unknown-secure-boot", "unknown-secure-boot", func(m nvidiaManager, r *fakeNvidiaRunner) { r.errors["mokutil --sb-state"] = errors.New("unknown") }},
		{"leap16", "unsupported-system", func(m nvidiaManager, r *fakeNvidiaRunner) {
			key := "rpm -qa --qf %{NAME}|%{VERSION}\n"
			r.outputs[key] = strings.ReplaceAll(r.outputs[key], "Leap-release|16.1", "Leap-release|16.0")
		}},
		{"mixed-gpus", "unsupported-gpu", func(m nvidiaManager, r *fakeNvidiaRunner) {
			r.outputs["lspci -Dnd 10de:"] += "\n0000:02:00.0 0300: 10de:1c82"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, r := officialManager(t)
			tc.change(m, r)
			status, err := m.status()
			if err != nil || status.State != tc.state {
				t.Fatalf("%+v %v", status, err)
			}
		})
	}
}

func TestNvidiaFreshInstallEligibility(t *testing.T) {
	r := compatibleRunner()
	m := nvidiaManager{run: r, recoveryPath: filepath.Join(t.TempDir(), "none")}
	status, kernel, err := m.inspect()
	if err != nil || !nvidiaInstallable(status) || kernel != "6.12.0-160100.7-default" {
		t.Fatalf("%+v %q %v", status, kernel, err)
	}
	if !strings.Contains(status.Detail, "NVIDIA "+nvidiaIntegrationVersion+" · Kernel 6.12.0-160100.7-default") {
		t.Fatalf("detail: %q", status.Detail)
	}
	r.outputs["modinfo -k 6.12.0-160100.7-default -F version nvidia"] = nvidiaIntegrationVersion
	status, err = m.status()
	if err != nil || status.State != "conflict" || nvidiaInstallable(status) {
		t.Fatalf("unowned .run module: %+v %v", status, err)
	}
}

// On a clean system modinfo fails and prints "Module nvidia not found."; the
// runner returns that text with the error. It is not a module version.
func TestNvidiaFreshInstallIgnoresModinfoNotFound(t *testing.T) {
	r := compatibleRunner()
	key := "modinfo -k 6.12.0-160100.7-default -F version nvidia"
	r.outputs[key] = "modinfo: ERROR: Module nvidia not found."
	r.errors[key] = errors.New("modinfo: exit status 1: modinfo: ERROR: Module nvidia not found.")
	m := nvidiaManager{run: r, recoveryPath: filepath.Join(t.TempDir(), "none")}
	status, err := m.status()
	if err != nil || status.State != "available" || !nvidiaInstallable(status) {
		t.Fatalf("%+v %v", status, err)
	}
}

// A first installation is not pinned to one kernel release: any stock -default
// kernel that is also the default boot kernel is eligible, and the reviewed
// solver plan later proves the signed KMP exists for it. Other flavors and a
// pending kernel switch are refused before any installation.
func TestNvidiaFreshInstallFollowsDefaultBootKernel(t *testing.T) {
	for _, tc := range []struct {
		name, kernel, vmlinuz, state string
	}{
		{"shipped", "6.12.0-160100.7-default", "/usr/lib/modules/6.12.0-160100.7-default/vmlinuz", "available"},
		{"next-update", "6.12.0-160100.12-default", "/boot/vmlinuz-6.12.0-160100.12-default", "available"},
		{"pending-reboot", "6.12.0-160100.7-default", "/boot/vmlinuz-6.12.0-160100.12-default", "kernel-missing"},
		{"non-default-flavor", "6.12.0-160100.7-rt", "/boot/vmlinuz-6.12.0-160100.7-rt", "unknown-boot-kernel"},
		{"custom-kernel", "6.17.1-lyra-default", "/boot/vmlinuz-6.17.1-lyra-default", "available"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := compatibleRunner()
			r.outputs["uname -r"] = tc.kernel
			r.outputs["readlink -f /boot/vmlinuz"] = tc.vmlinuz
			m := nvidiaManager{run: r, recoveryPath: filepath.Join(t.TempDir(), "none")}
			status, err := m.status()
			if err != nil || status.State != tc.state {
				t.Fatalf("%+v %v", status, err)
			}
			if nvidiaInstallable(status) != (tc.state == "available") {
				t.Fatalf("installable=%v for %s", nvidiaInstallable(status), tc.state)
			}
		})
	}
}

func TestNvidiaUnknownPCIIsNotGuessed(t *testing.T) {
	if supportedNvidiaDevice(0x2fff) {
		t.Fatal("unknown device ID accepted by numeric range")
	}
}

func TestNvidiaCheckNeverChangesSuspendPolicy(t *testing.T) {
	m, r := officialManager(t)
	data := []byte(nvidiaSleepQuarantineMarker + "\n[Sleep]\nAllowSuspend=no\n")
	if err := os.WriteFile(m.quarantinePath(), data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := m.check(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(m.quarantinePath())
	if err != nil || string(after) != string(data) {
		t.Fatal("public query modified suspend policy")
	}
	for _, call := range r.calls {
		if strings.HasPrefix(call, "systemctl") || strings.HasPrefix(call, "pkcheck") || strings.HasPrefix(call, "zypper") {
			t.Fatal(call)
		}
	}
}

func TestNvidiaCancellationHasNoDependencies(t *testing.T) {
	id, err := (&SoftwareService{}).InstallNvidia("", false)
	if id != 0 || err == nil || err.Name != BusName+".Error.Cancelled" {
		t.Fatalf("%d %v", id, err)
	}
}

func TestNvidiaDeniedAuthorizationNeverStartsTransaction(t *testing.T) {
	bin := t.TempDir()
	captured := filepath.Join(bin, "args")
	t.Setenv("PATH", bin)
	t.Setenv("NVIDIA_TEST_AUTH", captured)
	if err := os.WriteFile(filepath.Join(bin, "pkcheck"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$NVIDIA_TEST_AUTH\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// No activity/provider/bus: touching transaction machinery would panic.
	s := &SoftwareService{profile: profile.Desktop}
	id, err := s.InstallNvidia(":1.42", true)
	if id != 0 || err == nil || err.Name != BusName+".Error.AuthorizationFailed" {
		t.Fatalf("%d %v", id, err)
	}
	data, readErr := os.ReadFile(captured)
	if readErr != nil || !strings.Contains(string(data), "--system-bus-name\n:1.42\n--allow-user-interaction\n") {
		t.Fatalf("%s %v", data, readErr)
	}
}

func TestNvidiaBIOSDoesNotRequireUEFI(t *testing.T) {
	m, r := officialManager(t)
	r.outputs["mokutil --sb-state"] = "EFI variables are not supported on this system"
	r.errors["mokutil --sb-state"] = errors.New("no EFI")
	status, err := m.status()
	if err != nil || status.State != "active" || status.SecureBoot != "not-applicable" {
		t.Fatalf("%+v %v", status, err)
	}
}
