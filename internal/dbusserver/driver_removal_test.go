package dbusserver

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestRetiredDriverOperationsRejectEveryCallerWithoutDependencies(t *testing.T) {
	// No activity, connection, provider or command runner is initialized:
	// retired endpoints must reject before accessing any system dependency.
	software := &SoftwareService{}
	hardware := &HardwareService{}
	for _, sender := range []dbus.Sender{"", ":1.42"} {
		for _, confirmed := range []bool{false, true} {
			id, err := software.InstallNvidia(sender, confirmed)
			if id != 0 || err == nil || err.Name != "org.freedesktop.DBus.Error.NotSupported" {
				t.Fatalf("InstallNvidia(%q, %v) = %d, %v", sender, confirmed, id, err)
			}
			id, err = software.InstallNonFreeFirmware(sender, confirmed)
			if id != 0 || err == nil || err.Name != "org.freedesktop.DBus.Error.NotSupported" {
				t.Fatalf("InstallNonFreeFirmware(%q, %v) = %d, %v", sender, confirmed, id, err)
			}
		}
		for _, driver := range []string{"nouveau", "nvidia-open-driver-G06-signed-kmp-default", "invalid"} {
			err := hardware.SwitchNvidiaDriver(sender, driver)
			if err == nil || err.Name != "org.freedesktop.DBus.Error.NotSupported" {
				t.Fatalf("SwitchNvidiaDriver(%q, %q) = %v", sender, driver, err)
			}
		}
	}
}

func TestLegacyNvidiaStatusDoesNotOfferInstallation(t *testing.T) {
	runner := compatibleRunner()
	status, err := (nvidiaManager{run: runner}).status()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "unavailable" || status.Installed {
		t.Fatalf("legacy client could offer installation: %+v", status)
	}
}
