package dbusserver

import (
	"errors"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

// SnapshotsService drives an already installed snapshot tool. Snapper takes
// precedence; Timeshift is used as an optional fallback and is never installed
// or required by vegad.
type SnapshotsService struct {
	activity *Activity
	conn     *dbus.Conn
}

type SnapshotInfo struct {
	Id          uint32
	Timestamp   int64
	Trigger     string // "pre-pacman", "post-pacman", "manual", ...
	Description string
}

var errNoSnapshotTool = errors.New("snapper ou timeshift não está disponível neste sistema")

// Available reports whether one of the optional backends is already present.
func (s *SnapshotsService) Available() (bool, *dbus.Error) {
	s.activity.Touch()
	return snapperInstalled() || timeshiftInstalled(), nil
}

func (s *SnapshotsService) ListSnapshots() ([]SnapshotInfo, *dbus.Error) {
	s.activity.Touch()
	var snapshots []SnapshotInfo
	var err error
	if snapperInstalled() {
		snapshots, err = listSnapperSnapshots()
	} else if timeshiftInstalled() {
		snapshots, err = listTimeshiftSnapshots()
	} else {
		return nil, dbus.MakeFailedError(errNoSnapshotTool)
	}
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	return snapshots, nil
}

func (s *SnapshotsService) CreateSnapshot(sender dbus.Sender, description string) (uint32, *dbus.Error) {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.snapshots.create"); err != nil {
		return 0, err
	}
	var id uint32
	var err error
	if snapperInstalled() {
		id, err = createSnapperSnapshot("single", description)
	} else if timeshiftInstalled() {
		id, err = createTimeshiftSnapshot(description)
	} else {
		return 0, dbus.MakeFailedError(errNoSnapshotTool)
	}
	if err != nil {
		return 0, dbus.MakeFailedError(err)
	}
	return id, nil
}

// DiffPackages reports what would change (pending) if snapshotID were
// restored, so the confirmation screen can show it before rollback.
func (s *SnapshotsService) DiffPackages(snapshotID uint32) ([]string, *dbus.Error) {
	s.activity.Touch()
	if !snapperInstalled() && !timeshiftInstalled() {
		return nil, dbus.MakeFailedError(errNoSnapshotTool)
	}
	if timeshiftInstalled() && !snapperInstalled() {
		return []string{"Timeshift não fornece um diff de pacotes; revise o snapshot e o destino antes do rollback."}, nil
	}
	lines, err := snapperDiffLines(snapshotID)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	return lines, nil
}

func (s *SnapshotsService) Rollback(sender dbus.Sender, snapshotID uint32) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.snapshots.rollback"); err != nil {
		return err
	}
	var rollbackErr error
	if snapperInstalled() {
		rollbackErr = rollbackSnapperSnapshot(snapshotID)
	} else if timeshiftInstalled() {
		rollbackErr = rollbackTimeshiftSnapshot(snapshotID)
	} else {
		return dbus.MakeFailedError(errNoSnapshotTool)
	}
	if rollbackErr != nil {
		return dbus.MakeFailedError(rollbackErr)
	}
	return nil
}

func (s *SnapshotsService) DeleteSnapshot(sender dbus.Sender, snapshotID uint32) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.snapshots.delete"); err != nil {
		return err
	}
	var deleteErr error
	if snapperInstalled() {
		deleteErr = deleteSnapperSnapshot(snapshotID)
	} else if timeshiftInstalled() {
		deleteErr = deleteTimeshiftSnapshot(snapshotID)
	} else {
		return dbus.MakeFailedError(errNoSnapshotTool)
	}
	if deleteErr != nil {
		return dbus.MakeFailedError(deleteErr)
	}
	return nil
}

func (s *SnapshotsService) SetRetentionPolicy(sender dbus.Sender, keepCount uint32) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.snapshots.configure"); err != nil {
		return err
	}
	if !snapperInstalled() {
		if timeshiftInstalled() {
			return dbus.MakeFailedError(errors.New("Timeshift gerencia retenção por agenda e não oferece um limite global pela CLI"))
		}
		return dbus.MakeFailedError(errNoSnapshotTool)
	}
	if err := setSnapperRetentionPolicy(keepCount); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (s *SnapshotsService) formatSnapshot(snapshot SnapshotInfo) string {
	return fmt.Sprintf("%d %s %s", snapshot.Id, snapshot.Trigger, strings.TrimSpace(snapshot.Description))
}
