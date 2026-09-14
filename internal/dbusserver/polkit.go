package dbusserver

import (
	"fmt"
	"os/exec"

	"github.com/godbus/dbus/v5"
)

func requirePolkit(sender dbus.Sender, actionID string) *dbus.Error {
	return checkPolkit(sender, actionID, true)
}

// Read-only queries must never raise an authentication dialog, including when
// a local administrator overrides the packaged policy. The active desktop is
// allowed by the read policy; other callers receive a normal access error.
func requirePolkitRead(sender dbus.Sender, actionID string) *dbus.Error {
	return checkPolkit(sender, actionID, false)
}

func checkPolkit(sender dbus.Sender, actionID string, interactive bool) *dbus.Error {
	if sender == "" {
		return dbus.NewError(BusName+".Error.AuthorizationFailed", []interface{}{"chamada sem remetente D-Bus"})
	}
	if !commandAvailable("pkcheck") {
		return dbus.NewError(BusName+".Error.AuthorizationFailed", []interface{}{"pkcheck não está disponível"})
	}

	args := []string{
		"--action-id", actionID,
		"--system-bus-name", string(sender),
	}
	if interactive {
		args = append(args, "--allow-user-interaction")
	}
	cmd := exec.Command("pkcheck", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return dbus.NewError(BusName+".Error.AuthorizationFailed", []interface{}{fmt.Sprintf("%s: %s", err, string(out))})
	}
	return nil
}
