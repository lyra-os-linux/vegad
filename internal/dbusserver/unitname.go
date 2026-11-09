package dbusserver

import (
	"regexp"

	"github.com/godbus/dbus/v5"
)

// unitNamePattern is systemd's unit name alphabet (systemd.unit(5)): ASCII
// letters, digits, ":", "_", ".", "\", "@" and "-". The first character is
// never "-", so a name cannot be read as a systemctl option.
var unitNamePattern = regexp.MustCompile(`^[A-Za-z0-9:_.\\@][A-Za-z0-9:_.\\@-]{0,254}$`)

// validUnitName checks a unit name received over D-Bus before it reaches
// systemctl. Callers still pass "--" before it.
func validUnitName(name string) bool {
	return unitNamePattern.MatchString(name)
}

func invalidUnitName(name string) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", []interface{}{
		"nome de serviço inválido: " + name,
	})
}
