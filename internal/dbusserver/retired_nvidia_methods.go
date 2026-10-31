package dbusserver

import "github.com/godbus/dbus/v5"

// The NVIDIA integration was retired from Vega. These methods keep their v1
// wire signatures, deprecated in lyra-vega-dbus, so older clients get a
// handleable NotSupported instead of an unknown method. Like
// SwitchNvidiaDriver, they reject before authorization, query workers or any
// system access, and they are never routed (disabledOperation).

// NvidiaStatus is kept only for the (bbbssssu) reply signature.
type NvidiaStatus struct {
	Supported        bool
	Installed        bool
	RebootRequired   bool
	GPU              string
	SecureBoot       string
	State            string
	Detail           string
	RecoverySnapshot uint32
}

// NvidiaRecovery is kept only for the (bssss) reply signature.
type NvidiaRecovery struct {
	Available bool
	Kind      string
	Reference string
	State     string
	Detail    string
}

func (s *SoftwareService) NvidiaStatus() (NvidiaStatus, *dbus.Error) {
	return NvidiaStatus{}, nvidiaIntegrationRemoved()
}

func (s *SoftwareService) NvidiaRecovery() (NvidiaRecovery, *dbus.Error) {
	return NvidiaRecovery{}, nvidiaIntegrationRemoved()
}

func (s *SoftwareService) InstallNvidia(_ dbus.Sender, _ bool) (uint32, *dbus.Error) {
	return 0, nvidiaIntegrationRemoved()
}

func (s *SoftwareService) CheckNvidia() (bool, string, *dbus.Error) {
	return false, "", nvidiaIntegrationRemoved()
}
