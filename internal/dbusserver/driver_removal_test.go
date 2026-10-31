package dbusserver

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

const dbusNotSupported = "org.freedesktop.DBus.Error.NotSupported"

func TestRetiredDriverOperationsRejectEveryCallerWithoutDependencies(t *testing.T) {
	// No activity, connection, provider or command runner is initialized:
	// retired endpoints must reject before accessing any system dependency.
	software := &SoftwareService{}
	hardware := &HardwareService{}
	for _, sender := range []dbus.Sender{"", ":1.42"} {
		for _, confirmed := range []bool{false, true} {
			id, err := software.InstallNonFreeFirmware(sender, confirmed)
			if id != 0 || err == nil || err.Name != dbusNotSupported {
				t.Fatalf("InstallNonFreeFirmware(%q, %v) = %d, %v", sender, confirmed, id, err)
			}
		}
		for _, driver := range []string{"nouveau", "nvidia-open-driver-G06-signed-kmp-default", "invalid"} {
			err := hardware.SwitchNvidiaDriver(sender, driver)
			if err == nil || err.Name != dbusNotSupported {
				t.Fatalf("SwitchNvidiaDriver(%q, %q) = %v", sender, driver, err)
			}
		}
	}
}

func TestRetiredNvidiaMethodsAnswerNotSupportedWithoutPolkit(t *testing.T) {
	// An empty PATH makes pkcheck, systemd-run and every other tool
	// unavailable: a polkit check would answer AuthorizationFailed and a
	// worker or command would fail, never NotSupported.
	t.Setenv("PATH", t.TempDir())
	software := &SoftwareService{}
	hardware := &HardwareService{}
	for _, sender := range []dbus.Sender{"", ":1.42"} {
		for _, confirmed := range []bool{false, true} {
			if id, err := software.InstallNvidia(sender, confirmed); id != 0 || err == nil || err.Name != dbusNotSupported {
				t.Fatalf("InstallNvidia(%q, %v) = %d, %v", sender, confirmed, id, err)
			}
		}
		if err := hardware.SwitchNvidiaDriver(sender, "nvidia"); err == nil || err.Name != dbusNotSupported {
			t.Fatalf("SwitchNvidiaDriver(%q) = %v", sender, err)
		}
	}
	if status, err := software.NvidiaStatus(); err == nil || err.Name != dbusNotSupported || status != (NvidiaStatus{}) {
		t.Fatalf("NvidiaStatus() = %+v, %v", status, err)
	}
	if recovery, err := software.NvidiaRecovery(); err == nil || err.Name != dbusNotSupported || recovery != (NvidiaRecovery{}) {
		t.Fatalf("NvidiaRecovery() = %+v, %v", recovery, err)
	}
	if ok, detail, err := software.CheckNvidia(); ok || detail != "" || err == nil || err.Name != dbusNotSupported {
		t.Fatalf("CheckNvidia() = %v, %q, %v", ok, detail, err)
	}
	// The router must export them as they are: no polkit action and no
	// query worker in front of the NotSupported answer.
	for _, method := range []string{"Software.InstallNvidia", "Software.NvidiaStatus", "Software.NvidiaRecovery", "Software.CheckNvidia", "Hardware.SwitchNvidiaDriver"} {
		policy, known := methodPolicies[method]
		if !known || policy.privilege != disabledOperation || policy.action != "" {
			t.Fatalf("%s must stay a disabled operation, got %+v (known %v)", method, policy, known)
		}
	}
}
