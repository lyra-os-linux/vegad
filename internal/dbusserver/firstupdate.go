package dbusserver

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/distro"
	"github.com/lyraos/vegad/internal/profile"
)

const (
	defaultFirstUpdateMarkerPath = "/var/lib/vega/first-update.done"
	firstUpdateUnit              = "vegad-first-update.service"
	// ZYPPER_EXIT_ZYPP_LOCKED: another process holds the libzypp lock.
	zypperExitLocked = 7
)

// errFirstUpdateInProgress is what the desktop shows while the first-boot
// update holds the Zypper lock, instead of Zypper's raw "System management
// is locked" output.
var errFirstUpdateInProgress = errors.New("O sistema está aplicando a atualização inicial. Tente novamente em alguns minutos.")

// firstUpdateRunning reports whether vegad-first-update.service is running.
// While it waits for a retry (RestartSec) the unit is "activating", not
// "active", and holds no lock. A variable so tests can stub systemctl.
var firstUpdateRunning = func() bool {
	return commandAvailable("systemctl") &&
		systemCommand("systemctl", "is-active", "--quiet", firstUpdateUnit).Run() == nil
}

// requireFirstUpdateIdle refuses a native package transaction up front while
// the first-boot update runs, before a Polkit prompt or a Snapper pre
// snapshot is spent on an operation Zypper would reject.
func requireFirstUpdateIdle() *dbus.Error {
	if firstUpdateRunning() {
		return firstUpdateBusyError()
	}
	return nil
}

func firstUpdateBusyError() *dbus.Error {
	return dbus.NewError(BusName+".Error.FirstUpdateInProgress", []interface{}{errFirstUpdateInProgress.Error()})
}

// explainFirstUpdateLock turns a Zypper lock failure into
// errFirstUpdateInProgress when the first-boot update is the lock holder. It
// covers reads (search, details, updates) and a transaction that raced the
// unit's start; any other failure is returned unchanged.
func explainFirstUpdateLock(err error) error {
	var exitErr *exec.ExitError
	if err != nil && errors.As(err, &exitErr) && exitErr.ExitCode() == zypperExitLocked && firstUpdateRunning() {
		return errFirstUpdateInProgress
	}
	return err
}

// packageQueryError is dbus.MakeFailedError for native package reads, keeping
// the dedicated error name when the first-boot update holds the lock.
func packageQueryError(err error) *dbus.Error {
	if err = explainFirstUpdateLock(err); errors.Is(err, errFirstUpdateInProgress) {
		return firstUpdateBusyError()
	}
	return dbus.MakeFailedError(err)
}

func firstUpdateMarkerPath() string {
	if path := os.Getenv("VEGAD_FIRST_UPDATE_MARKER"); path != "" {
		return path
	}
	return defaultFirstUpdateMarkerPath
}

func trustedKeyringPath() string {
	if path := os.Getenv("VEGAD_TRUSTED_KEYRING"); path != "" {
		return path
	}
	return distro.DefaultTrustedKeyringPath
}

// isLiveCmdline reports whether the kernel command line boots the KIWI live
// ISO. The unit already has ConditionKernelCommandLine=!rd.live.image; this
// is the second guard for a manual `vegad first-update` in the live session.
func isLiveCmdline(cmdline string) bool {
	for _, arg := range strings.Fields(cmdline) {
		if arg == "rd.live.image" || strings.HasPrefix(arg, "root=live:") {
			return true
		}
	}
	return false
}

func writeFirstUpdateMarker(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("done\n"), 0o644)
}

// RunFirstUpdateJob runs once on the first boot of an installed system:
// imports the pinned package-signing keys, refreshes every repository and
// applies all pending package updates inside a Snapper pre/post pair. The
// marker is written only after everything succeeds, so a failure (no
// network, unknown repository key, solver problem) is retried by systemd and
// on the next boot. It is invoked by vegad-first-update.service through
// `vegad first-update`, not through the bus-activated Server.
//
// Zypper is never given --gpg-auto-import-keys: a repository whose key is
// not pinned makes the refresh fail, and the user approves that key's full
// fingerprint in Vega like any other repository key.
func RunFirstUpdateJob(activeProfile profile.Profile) error {
	marker := firstUpdateMarkerPath()
	if _, err := os.Stat(marker); err == nil {
		log.Printf("vegad: atualização inicial já concluída (%s)", marker)
		return nil
	}
	if cmdline, err := os.ReadFile("/proc/cmdline"); err == nil && isLiveCmdline(string(cmdline)) {
		log.Printf("vegad: atualização inicial ignorada na sessão live")
		return nil
	}

	id, err := distro.Detect()
	if err != nil {
		return err
	}
	provider, err := distro.NewProvider(id)
	if err != nil {
		return err
	}

	log.Printf("vegad: importando chaves de assinatura confiáveis")
	if err := distro.ImportTrustedPackageKeys(trustedKeyringPath()); err != nil {
		return err
	}
	log.Printf("vegad: atualizando metadados dos repositórios")
	if err := provider.Package().SyncDatabase(); err != nil {
		return err
	}

	lastPercent := uint32(101)
	report := func(percent uint32, message string) {
		if percent != lastPercent {
			log.Printf("vegad: atualização inicial %d%% %s", percent, message)
			lastPercent = percent
		}
	}
	pkgReport := func(string, distro.PackagePhase, uint32) {}
	if err := withSnapshots("Atualização inicial", func() error {
		return provider.Package().UpdateAll(report, pkgReport)
	}); err != nil {
		return err
	}

	if err := writeFirstUpdateMarker(marker); err != nil {
		return fmt.Errorf("registrar atualização inicial: %w", err)
	}
	log.Printf("vegad: atualização inicial concluída")

	// Refresh the cached count so the desktop stops showing the updates that
	// were just applied. Failing here does not undo the completed update.
	if err := RunUpdateCheckJob(activeProfile); err != nil {
		log.Printf("vegad: atualizar estado após a atualização inicial: %v", err)
	}
	return nil
}
