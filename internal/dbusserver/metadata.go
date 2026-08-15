package dbusserver

import (
	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/profile"
	"github.com/lyraos/vegad/internal/version"
)

// MetadataService exposes stable feature discovery. Capabilities describe
// product policy; clients must not treat them as an authorization decision.
type MetadataService struct {
	activity *Activity
	profile  profile.Profile
}

func (s *MetadataService) Profile() (string, *dbus.Error) {
	s.activity.Touch()
	return string(s.profile), nil
}

func (s *MetadataService) Version() (string, *dbus.Error) {
	s.activity.Touch()
	return version.Version, nil
}

func (s *MetadataService) Capabilities() ([]string, *dbus.Error) {
	s.activity.Touch()
	return s.profile.Capabilities(), nil
}
