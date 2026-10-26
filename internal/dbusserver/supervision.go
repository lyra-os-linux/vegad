package dbusserver

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

// supervisedGroup marks accounts supervised by Lyra OS basic parental
// controls. The group comes from the lyra-parental-controls package, which
// also blocks TTY/SSH logins and locks Alt+F2 for its members. The rules
// (apps, installation, age rating) belong to malcontent and its "Controle
// parental" application.
const supervisedGroup = "lyra-supervised"

// supervisedHiddenPaths are added to a supervised account's malcontent app
// filter so GNOME Shell and Sheliak hide the terminals and Vega from it.
// malcontent only hides them; it does not stop direct execution.
var supervisedHiddenPaths = []string{
	"/usr/bin/kgx",
	"/usr/bin/gnome-terminal",
	"/usr/bin/fish",
	"/usr/bin/vega-gtk",
}

func containsGroup(groups []string, group string) bool {
	for _, g := range groups {
		if g == group {
			return true
		}
	}
	return false
}

// checkSupervision validates the final group set of username. A supervised
// account cannot be an administrator, and the last administrator cannot lose
// wheel, so the machine always keeps someone who can lift the supervision.
func checkSupervision(username string, finalGroups []string, admins map[string]bool) error {
	supervised := containsGroup(finalGroups, supervisedGroup)
	admin := containsGroup(finalGroups, "wheel")
	if supervised && admin {
		return fmt.Errorf("uma conta supervisionada não pode ser administradora")
	}
	if admins[username] && !admin {
		others := 0
		for member := range admins {
			if member != username && member != "root" {
				others++
			}
		}
		if others == 0 {
			return fmt.Errorf("%s é a última conta administradora e não pode deixar de sê-lo", username)
		}
	}
	return nil
}

// withSupervisionPaths adds (supervised) or removes (not supervised) the
// supervisedHiddenPaths in a malcontent blocklist, keeping every other entry
// the administrator set. Allowlist filters are left untouched: there the
// terminals are already denied unless the administrator listed them.
func withSupervisionPaths(allowlist bool, entries []string, supervised bool) []string {
	if allowlist {
		return entries
	}
	ours := map[string]bool{}
	for _, path := range supervisedHiddenPaths {
		ours[path] = true
	}
	result := make([]string, 0, len(entries)+len(supervisedHiddenPaths))
	for _, entry := range entries {
		if !ours[entry] {
			result = append(result, entry)
		}
	}
	if supervised {
		result = append(result, supervisedHiddenPaths...)
	}
	return result
}

type appFilter struct {
	Allowlist bool
	Entries   []string
}

const (
	accountsBus       = "org.freedesktop.Accounts"
	appFilterIface    = "com.endlessm.ParentalControls.AppFilter"
	appFilterProperty = appFilterIface + ".AppFilter"
)

// applySupervisionFilter updates username's malcontent app filter through
// AccountsService, which vegad may write as root.
func applySupervisionFilter(conn *dbus.Conn, username string, supervised bool) error {
	if conn == nil {
		return fmt.Errorf("barramento do sistema indisponível")
	}
	var path dbus.ObjectPath
	if err := conn.Object(accountsBus, "/org/freedesktop/Accounts").
		Call("org.freedesktop.Accounts.FindUserByName", 0, username).Store(&path); err != nil {
		return fmt.Errorf("AccountsService não encontrou %s: %w", username, err)
	}
	user := conn.Object(accountsBus, path)
	variant, err := user.GetProperty(appFilterProperty)
	if err != nil {
		return fmt.Errorf("lendo o controle parental de %s: %w", username, err)
	}
	var current appFilter
	if err := dbus.Store([]interface{}{variant.Value()}, &current); err != nil {
		return fmt.Errorf("controle parental de %s em formato inesperado: %w", username, err)
	}
	next := appFilter{Allowlist: current.Allowlist, Entries: withSupervisionPaths(current.Allowlist, current.Entries, supervised)}
	if err := user.SetProperty(appFilterProperty, dbus.MakeVariant(next)); err != nil {
		return fmt.Errorf("gravando o controle parental de %s: %w", username, err)
	}
	return nil
}
