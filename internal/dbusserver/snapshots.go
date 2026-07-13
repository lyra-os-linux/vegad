package dbusserver

import (
	"errors"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

// SnapshotsService backs org.lyraos.Vega1.Snapshots: orchestrates whichever
// snapshot tool is present — snapper (Arch/openSUSE, Btrfs) or Timeshift
// (Debian/Ubuntu, see timeshift.go) — for the "Voltar no tempo" timeline,
// manual snapshots and retention policy. Dispatch is by tool presence, not
// distro ID, same pattern FirewallService uses for firewalld/ufw. vegad
// does not reimplement either tool — it drives their own CLIs.
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

var errNoSnapshotTool = errors.New("nenhuma ferramenta de snapshot (snapper ou timeshift) disponível")

func (s *SnapshotsService) ListSnapshots() ([]SnapshotInfo, *dbus.Error) {
	s.activity.Touch()
	if snapperInstalled() {
		snapshots, err := listSnapperSnapshots()
		if err != nil {
			return nil, dbus.MakeFailedError(err)
		}
		return snapshots, nil
	}
	if timeshiftInstalled() {
		snapshots, err := listTimeshiftSnapshots()
		if err != nil {
			return nil, dbus.MakeFailedError(err)
		}
		return snapshots, nil
	}
	return nil, dbus.MakeFailedError(errNoSnapshotTool)
}

func (s *SnapshotsService) CreateSnapshot(sender dbus.Sender, description string) (uint32, *dbus.Error) {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.snapshots.create"); err != nil {
		return 0, err
	}
	if snapperInstalled() {
		id, err := createSnapperSnapshot("single", description)
		if err != nil {
			return 0, dbus.MakeFailedError(err)
		}
		return id, nil
	}
	if timeshiftInstalled() {
		id, err := createTimeshiftSnapshot(description)
		if err != nil {
			return 0, dbus.MakeFailedError(err)
		}
		return id, nil
	}
	return 0, dbus.MakeFailedError(errNoSnapshotTool)
}

// DiffPackages reports what would change (pending) if snapshotID were
// restored, so the confirmation screen can show it before rollback.
// Timeshift has no equivalent to snapper's package-aware diff — see
// timeshiftDiffPackages.
func (s *SnapshotsService) DiffPackages(snapshotID uint32) ([]string, *dbus.Error) {
	s.activity.Touch()
	if snapperInstalled() {
		lines, err := snapperDiffLines(snapshotID)
		if err != nil {
			return nil, dbus.MakeFailedError(err)
		}
		return lines, nil
	}
	if timeshiftInstalled() {
		return timeshiftDiffPackages(), nil
	}
	return nil, dbus.MakeFailedError(errNoSnapshotTool)
}

func (s *SnapshotsService) Rollback(sender dbus.Sender, snapshotID uint32) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.snapshots.rollback"); err != nil {
		return err
	}
	if snapperInstalled() {
		if err := rollbackSnapperSnapshot(snapshotID); err != nil {
			return dbus.MakeFailedError(err)
		}
		return nil
	}
	if timeshiftInstalled() {
		if err := rollbackTimeshiftSnapshot(snapshotID); err != nil {
			return dbus.MakeFailedError(err)
		}
		return nil
	}
	return dbus.MakeFailedError(errNoSnapshotTool)
}

func (s *SnapshotsService) DeleteSnapshot(sender dbus.Sender, snapshotID uint32) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.snapshots.delete"); err != nil {
		return err
	}
	if snapperInstalled() {
		if err := deleteSnapperSnapshot(snapshotID); err != nil {
			return dbus.MakeFailedError(err)
		}
		return nil
	}
	if timeshiftInstalled() {
		if err := deleteTimeshiftSnapshot(snapshotID); err != nil {
			return dbus.MakeFailedError(err)
		}
		return nil
	}
	return dbus.MakeFailedError(errNoSnapshotTool)
}

func (s *SnapshotsService) SetRetentionPolicy(sender dbus.Sender, keepCount uint32) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.snapshots.configure"); err != nil {
		return err
	}
	if snapperInstalled() {
		if err := setSnapperRetentionPolicy(keepCount); err != nil {
			return dbus.MakeFailedError(err)
		}
		return nil
	}
	if timeshiftInstalled() {
		if err := setTimeshiftRetentionPolicy(keepCount); err != nil {
			return dbus.MakeFailedError(err)
		}
		return nil
	}
	return dbus.MakeFailedError(errNoSnapshotTool)
}

func (s *SnapshotsService) formatSnapshot(snapshot SnapshotInfo) string {
	return fmt.Sprintf("%d %s %s", snapshot.Id, snapshot.Trigger, strings.TrimSpace(snapshot.Description))
}
