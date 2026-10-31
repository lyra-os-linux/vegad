package dbusserver

import "github.com/godbus/dbus/v5"

// Retain the public v1 method signatures while refusing retired operations
// before authentication, subprocess execution or transaction allocation.
func driverManagementRemoved() *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.NotSupported", []interface{}{
		"A instalação de drivers e firmware e a troca de drivers foram removidas do Vega.",
	})
}

// nvidiaIntegrationRemoved answers the retired NVIDIA methods; see
// retired_nvidia_methods.go.
func nvidiaIntegrationRemoved() *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.NotSupported", []interface{}{
		"A integração NVIDIA foi removida do Vega; siga o procedimento oficial do openSUSE.",
	})
}

// errNotImplemented marks a method that exists on the interface per the
// spec but whose backend (libalpm/snapper/restic/etc. orchestration) isn't
// wired up yet in this scaffold.
func errNotImplemented(method string) *dbus.Error {
	return dbus.NewError(BusName+".Error.NotImplemented", []interface{}{method + " not implemented yet"})
}
