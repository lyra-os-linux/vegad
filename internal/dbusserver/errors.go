package dbusserver

import "github.com/godbus/dbus/v5"

// errNotImplemented marks a method that exists on the interface per the
// spec but whose backend (libalpm/snapper/restic/etc. orchestration) isn't
// wired up yet in this scaffold.
func errNotImplemented(method string) *dbus.Error {
	return dbus.NewError(BusName+".Error.NotImplemented", []interface{}{method + " not implemented yet"})
}
