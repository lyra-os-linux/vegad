package dbusserver

import (
	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/version"
)

// SystemService backs org.lyraos.Vega1.System — the minimal interface the
// UI uses to confirm vegad is reachable before touching any privileged
// module.
type SystemService struct {
	activity *Activity
}

func (s *SystemService) Version() (string, *dbus.Error) {
	s.activity.Touch()
	return version.Version, nil
}

func (s *SystemService) Ping() (bool, *dbus.Error) {
	s.activity.Touch()
	return true, nil
}
