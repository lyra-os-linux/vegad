package dbusserver

import (
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

// SnapshotsService backs org.lyraos.Vega1.Snapshots: orchestrates snapper
// for the "Voltar no tempo" timeline, manual
// snapshots and retention policy. vegad does not reimplement snapper —
// it drives snapper's own D-Bus API.
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

func (s *SnapshotsService) ListSnapshots() ([]SnapshotInfo, *dbus.Error) {
	s.activity.Touch()
	snapshots, err := listSnapperSnapshots()
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
	id, err := createSnapperSnapshot("single", description)
	if err != nil {
		return 0, dbus.MakeFailedError(err)
	}
	return id, nil
}

// DiffPackages reports what would change (pending) if snapshotID were
// restored, so the confirmation screen can show it before rollback.
func (s *SnapshotsService) DiffPackages(snapshotID uint32) ([]string, *dbus.Error) {
	s.activity.Touch()
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
	if err := rollbackSnapperSnapshot(snapshotID); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (s *SnapshotsService) DeleteSnapshot(sender dbus.Sender, snapshotID uint32) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.snapshots.delete"); err != nil {
		return err
	}
	if err := deleteSnapperSnapshot(snapshotID); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (s *SnapshotsService) SetRetentionPolicy(sender dbus.Sender, keepCount uint32) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.snapshots.configure"); err != nil {
		return err
	}
	if err := setSnapperRetentionPolicy(keepCount); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (s *SnapshotsService) formatSnapshot(snapshot SnapshotInfo) string {
	return fmt.Sprintf("%d %s %s", snapshot.Id, snapshot.Trigger, strings.TrimSpace(snapshot.Description))
}
