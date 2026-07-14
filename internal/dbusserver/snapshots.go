package dbusserver

import (
	"errors"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

// SnapshotsService backs org.lyraos.Vega1.Snapshots: drives snapper
// (Arch/openSUSE, Btrfs) for the "Voltar no tempo" timeline, manual
// snapshots and retention policy. vegad does not reimplement snapper — it
// drives its CLI. Timeshift (Debian/Ubuntu/Fedora) was dropped as a backend
// (issue #48): its `--list` table format never matched a real installation
// and left every non-snapper distro unable to list existing snapshots.
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

var errNoSnapshotTool = errors.New("snapper não está disponível neste sistema")

// Available reports whether snapper is present, so the frontend can hide
// the Snapshots module entirely on distros without it instead of showing a
// menu entry that always errors.
func (s *SnapshotsService) Available() (bool, *dbus.Error) {
	s.activity.Touch()
	return snapperInstalled(), nil
}

func (s *SnapshotsService) ListSnapshots() ([]SnapshotInfo, *dbus.Error) {
	s.activity.Touch()
	if !snapperInstalled() {
		return nil, dbus.MakeFailedError(errNoSnapshotTool)
	}
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
	if !snapperInstalled() {
		return 0, dbus.MakeFailedError(errNoSnapshotTool)
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
	if !snapperInstalled() {
		return nil, dbus.MakeFailedError(errNoSnapshotTool)
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
	if !snapperInstalled() {
		return dbus.MakeFailedError(errNoSnapshotTool)
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
	if !snapperInstalled() {
		return dbus.MakeFailedError(errNoSnapshotTool)
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
	if !snapperInstalled() {
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
