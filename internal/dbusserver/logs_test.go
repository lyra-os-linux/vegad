package dbusserver

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestLogsRejectUnauthorizedCallerBeforeJournalctl(t *testing.T) {
	run := false
	service := &LogsService{
		activity: &Activity{},
		authorize: func(dbus.Sender, string) *dbus.Error {
			return dbus.NewError(BusName+".Error.AuthorizationFailed", []interface{}{"denied"})
		},
		run: func(string, ...string) (string, error) {
			run = true
			return "secret", nil
		},
	}
	if _, err := service.Query(":1.5", "", "", "", "", 10); err == nil {
		t.Fatal("unauthorized query succeeded")
	}
	if run {
		t.Fatal("journalctl ran before authorization")
	}
	if _, err := service.ListUnits(":1.5"); err == nil {
		t.Fatal("unauthorized unit listing succeeded")
	}
}

func TestLogsAuthorizedCallerGetsAdministrativeJournal(t *testing.T) {
	service := &LogsService{
		activity:  &Activity{},
		authorize: func(dbus.Sender, string) *dbus.Error { return nil },
		run: func(_ string, args ...string) (string, error) {
			if len(args) > 1 && args[1] == "--field=_SYSTEMD_UNIT" {
				return "sshd.service\nvegad.service", nil
			}
			return "line one\nline two", nil
		},
	}
	lines, err := service.Query(":1.5", "sshd.service", "err", "-1hour", "failed", 20)
	if err != nil || len(lines) != 2 {
		t.Fatalf("authorized query = %#v, %v", lines, err)
	}
	units, err := service.ListUnits(":1.5")
	if err != nil || len(units) != 2 || units[0] != "sshd.service" {
		t.Fatalf("authorized units = %#v, %v", units, err)
	}
}
