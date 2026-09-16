package dbusserver

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/nvidiarecovery"
	"github.com/lyraos/vegad/internal/profile"
)

// NvidiaRecovery keeps the original NvidiaStatus/Snapper numeric ABI intact.
// Available means the strategy's preflight passed, not that a backup exists.
type NvidiaRecovery struct {
	Available bool
	Kind      string
	Reference string
	State     string
	Detail    string
}

func (m nvidiaManager) recovery(p profile.Profile) NvidiaRecovery {
	r := NvidiaRecovery{State: "unprepared"}
	record, err := nvidiarecovery.ReadRecord(filepath.Dir(m.recoveryFile()))
	if err == nil {
		r.Kind = record.Kind
		r.Reference = record.Reference
		r.State = record.State
	} else if !errors.Is(err, os.ErrNotExist) {
		r.Detail = err.Error()
		return r
	}
	if nvidiarecovery.RecoveryRequired() {
		r.Detail = "Complete the interrupted offline recovery before installing NVIDIA"
		return r
	}
	fs, err := m.run.Output("findmnt", "--noheadings", "--output", "FSTYPE", "--target", "/")
	if err != nil {
		r.Detail = "Cannot determine the recovery filesystem"
		return r
	}
	switch strings.TrimSpace(fs) {
	case "btrfs":
		r.Kind = "snapper"
		if _, err := os.Stat("/usr/bin/snapper"); err != nil {
			r.Detail = "Snapper is required"
			return r
		}
		// The privileged pre-commit operation validates the root configuration and
		// creates the snapshot. Reading its private configuration is not authorized.
		r.Available = true
		r.Detail = "A root Snapper pre-snapshot is required before RPM commit"
	case "ext4":
		r.Kind = "restic-offline"
		if p != profile.Server {
			r.Detail = "ext4 recovery is qualified for the Server profile"
			return r
		}
		if err := nvidiarecovery.Preflight(); err != nil {
			r.Detail = err.Error()
			return r
		}
		r.Available = true
		r.Detail = "Verified local OS backup; restore offline from rescue media; service data and ESP excluded"
	default:
		r.Detail = "No qualified NVIDIA recovery strategy for this filesystem"
	}
	return r
}

func (s *SoftwareService) NvidiaRecovery() (NvidiaRecovery, *dbus.Error) {
	s.activity.Touch()
	return newNvidiaManager().recovery(s.profile), nil
}
