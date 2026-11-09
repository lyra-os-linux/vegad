package dbusserver

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

// vegad#128: a unit name from D-Bus must never reach systemctl as an option.
func TestValidUnitName(t *testing.T) {
	for _, name := range []string{"sshd", "sshd.service", "vega-web.socket", "getty@tty1.service",
		"systemd-fsck@dev-disk-by\\x2duuid-1234.service", "user-runtime-dir@1000.service"} {
		if !validUnitName(name) {
			t.Errorf("valid unit refused: %q", name)
		}
	}
	for _, name := range []string{"", "-h", "--root=/tmp", "--global", "-", "ssh d.service",
		"sshd;reboot", "../etc/passwd", "sshd\n.service", string(make([]byte, 256))} {
		if validUnitName(name) {
			t.Errorf("invalid unit accepted: %q", name)
		}
	}
}

func TestServicesRefuseInvalidUnitBeforePolkit(t *testing.T) {
	s := &ServicesService{activity: &Activity{}}
	for name, err := range map[string]*dbus.Error{
		"SetServiceEnabled": s.SetServiceEnabled(":1.1", "--root=/tmp", true),
		"SetServiceRunning": s.SetServiceRunning(":1.1", "-h", true),
		"RestartService":    s.RestartService(":1.1", "--global"),
	} {
		if err == nil || err.Name != "org.freedesktop.DBus.Error.InvalidArgs" {
			t.Fatalf("%s: invalid unit name not refused with InvalidArgs: %v", name, err)
		}
	}
}
