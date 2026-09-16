package dbusserver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/distro"
	"github.com/lyraos/vegad/internal/nvidiarecovery"
	"github.com/lyraos/vegad/internal/profile"
)

var nvidiaInstallMu sync.Mutex

func (s *SoftwareService) InstallNvidia(sender dbus.Sender, confirmed bool) (uint32, *dbus.Error) {
	// A cancelled dialog must never authorize, create snapshots or touch repos.
	if !confirmed {
		return 0, dbus.NewError(BusName+".Error.Cancelled", []interface{}{"NVIDIA installation was not confirmed"})
	}
	if s.profile != profile.Desktop && s.profile != profile.Server {
		return 0, dbus.NewError("org.freedesktop.DBus.Error.NotSupported", []interface{}{"Unknown NVIDIA installation profile"})
	}
	if err := requirePolkit(sender, "org.lyraos.vega.software.install"); err != nil {
		return 0, err
	}
	if !nvidiaInstallMu.TryLock() {
		return 0, dbus.MakeFailedError(errors.New("NVIDIA installation already running"))
	}
	s.activity.Touch()
	id := s.startTransaction("Lyra NVIDIA", func(report progressFunc, _ packageProgressFunc) error {
		defer nvidiaInstallMu.Unlock()
		return newNvidiaManager().install(s.profile, report)
	})
	if id == 0 {
		nvidiaInstallMu.Unlock()
	}
	return id, nil
}

func nvidiaInstallable(status NvidiaStatus) bool {
	return status.Supported && (status.State == "available" || status.State == "unmanaged")
}

func (m nvidiaManager) install(p profile.Profile, report progressFunc) error {
	status, err := m.status()
	if err != nil {
		return err
	}
	if !nvidiaInstallable(status) {
		return fmt.Errorf("NVIDIA installation refused: %s (%s)", status.State, status.Detail)
	}
	recovery := m.recovery(p)
	if !recovery.Available {
		return fmt.Errorf("NVIDIA recovery unavailable: %s", recovery.Detail)
	}
	if err := distro.ValidateNvidiaRepositories("/etc/zypp/repos.d"); err != nil {
		return err
	}
	_, repoStatErr := os.Lstat("/etc/zypp/repos.d/lyra-nvidia-upstream.repo")
	newUpstreamRepo := errors.Is(repoStatErr, os.ErrNotExist)
	if repoStatErr != nil && !newUpstreamRepo {
		return repoStatErr
	}
	var snapshot uint32
	var point *nvidiarecovery.Point
	var record nvidiarecovery.Record
	beforeCommit := func() error {
		// The lock was acquired after metadata refresh. Recheck prerequisites so
		// concurrent package changes cannot silently invalidate the reviewed flow.
		latest, err := m.status()
		if err != nil {
			return err
		}
		if !nvidiaInstallable(latest) {
			return fmt.Errorf("NVIDIA state changed: %s", latest.State)
		}
		if err := distro.ValidateNvidiaRepositories("/etc/zypp/repos.d"); err != nil {
			return err
		}
		current := m.recovery(p)
		if !current.Available || current.Kind != recovery.Kind {
			return errors.New("NVIDIA recovery prerequisites changed")
		}
		// Zypper is waiting for confirmation while holding its package lock.
		// Do not permit RPM commit until the recovery point is verified.
		if recovery.Kind == "restic-offline" {
			report(45, "NVIDIA: preparing and verifying offline recovery")
			point, err = nvidiarecovery.Prepare(func() { report(45, "NVIDIA: verifying offline recovery") })
			if err != nil {
				return err
			}
			record = nvidiarecovery.Record{Kind: recovery.Kind, Reference: point.Manifest.Reference, State: "ready"}
		} else {
			snapshot, err = createSnapperSnapshot("pre", "Before Lyra NVIDIA installation")
			if err != nil || snapshot == 0 {
				return fmt.Errorf("NVIDIA recovery snapshot required: %v", err)
			}
			if err = writeNvidiaRecovery(m.recoveryFile(), snapshot); err != nil {
				return err
			}
			record = nvidiarecovery.Record{Kind: "snapper", Reference: strconv.FormatUint(uint64(snapshot), 10), State: "ready"}
		}
		return nvidiarecovery.WriteRecord(filepath.Dir(m.recoveryFile()), record)
	}
	installErr := distro.InstallNvidiaBundle(distro.ProgressFunc(report), beforeCommit)
	if installErr == nil {
		after, statusErr := m.status()
		if statusErr != nil {
			installErr = statusErr
		} else if !after.Installed || (after.State != "active" && after.State != "reboot-required") {
			installErr = fmt.Errorf("NVIDIA post-install verification: %s (%s)", after.State, after.Detail)
		} else {
			installErr = distro.EnableNvidiaOBS("/etc/zypp/repos.d")
		}
	}
	if installErr != nil && newUpstreamRepo && m.packageVersion("lyra-nvidia") == "" {
		if disableErr := distro.DisableUnprotectedNvidiaRepository("/etc/zypp/repos.d"); disableErr != nil {
			installErr = fmt.Errorf("%w; failed repository cleanup: %v", installErr, disableErr)
		}
	}
	var postErr error
	if snapshot != 0 {
		_, postErr = createSnapperSnapshot("post", "After Lyra NVIDIA installation", snapshot)
	}
	if record.Reference != "" {
		record.State = "installed"
		if installErr != nil {
			record.State = "failed"
		}
		if point != nil {
			postErr = errors.Join(postErr, point.Mark(record.State))
		}
		postErr = errors.Join(postErr, nvidiarecovery.WriteRecord(filepath.Dir(m.recoveryFile()), record))
	}
	if installErr != nil {
		if record.Reference == "" {
			return installErr
		}
		return fmt.Errorf("%w; recovery %s %s. Review packages and repositories before rebooting; no automatic rollback was attempted", errors.Join(installErr, postErr), record.Kind, record.Reference)
	}
	if postErr != nil {
		return fmt.Errorf("NVIDIA installed; recovery finalization failed: %w (%s %s)", postErr, record.Kind, record.Reference)
	}
	report(100, "Lyra NVIDIA: verified")
	return nil
}

// Public read-only status, in its own root-owned directory. Never write root
// state beneath the per-user query-cache directory /var/lib/vega.
func writeNvidiaRecovery(path string, id uint32) error {
	dir := filepath.Dir(path)
	if err := os.Mkdir(dir, 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || stat.Uid != 0 || info.Mode().Perm()&0022 != 0 {
		return errors.New("unsafe NVIDIA recovery directory")
	}
	if err := os.Chmod(dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".recovery-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(strconv.FormatUint(uint64(id), 10) + "\n")
	if err == nil {
		err = f.Chmod(0644)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
