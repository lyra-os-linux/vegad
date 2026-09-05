package dbusserver

import "github.com/godbus/dbus/v5"

// Retain the public v1 method signatures while refusing retired operations
// before authentication, subprocess execution or transaction allocation.
func driverManagementRemoved() *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.NotSupported", []interface{}{
		"A instalação de drivers e firmware e a troca de drivers foram removidas do Vega.",
	})
}

// errNotImplemented marks a method that exists on the interface per the
// spec but whose backend (libalpm/snapper/restic/etc. orchestration) isn't
// wired up yet in this scaffold.
func errNotImplemented(method string) *dbus.Error {
	return dbus.NewError(BusName+".Error.NotImplemented", []interface{}{method + " not implemented yet"})
}
