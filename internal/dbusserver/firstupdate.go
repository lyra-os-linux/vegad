package dbusserver

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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
var errFirstUpdateInProgress = errors.New("O sistema está preparando os repositórios. Tente novamente em alguns minutos.")

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

// Keep the exemption beside the completion marker, including in test roots.
func firstUpdateSkipPath() string {
	return filepath.Join(filepath.Dir(firstUpdateMarkerPath()), "first-update.skipped")
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

// RunFirstUpdateJob prepares repositories on the first installed boot. The
// historical command, service and marker names remain compatible with RPMs
// already shipped. Package installation belongs to the normal Vega workflow.
func RunFirstUpdateJob(activeProfile profile.Profile) error {
	marker := firstUpdateMarkerPath()
	if _, err := os.Stat(firstUpdateSkipPath()); err == nil {
		log.Printf("vegad: preparação dos repositórios dispensada para instalação legada")
		return nil
	}
	if _, err := os.Stat(marker); err == nil {
		log.Printf("vegad: preparação dos repositórios já concluída (%s)", marker)
		return nil
	}
	if cmdline, err := os.ReadFile("/proc/cmdline"); err == nil && isLiveCmdline(string(cmdline)) {
		log.Printf("vegad: preparação dos repositórios ignorada na sessão live")
		return nil
	}

	id, err := distro.Detect()
	if err != nil {
		return errors.Join(err, persistPreparationStatus(preparationStatePath(marker), preparationFailure("detecting-system", err, time.Now())))
	}
	provider, err := distro.NewProvider(id)
	if err != nil {
		return errors.Join(err, persistPreparationStatus(preparationStatePath(marker), preparationFailure("detecting-system", err, time.Now())))
	}

	return prepareInitialRepositories(activeProfile, marker, provider.Package(), func() error {
		return distro.ImportTrustedPackageKeys(trustedKeyringPath())
	}, publishUpdateStatus)
}

// This deliberately small interface excludes package installation operations.
type repositoryPreparer interface {
	SyncDatabase() error
	ListUpdates() ([]distro.PackageRef, error)
}

func prepareInitialRepositories(activeProfile profile.Profile, marker string, packages repositoryPreparer, importKeys func() error, publish func(UpdateStatus) error) (result error) {
	statePath := preparationStatePath(marker)
	phase := "importing-keys"
	stage := func(next string) error {
		phase = next
		return persistPreparationStatus(statePath, PreparationStatus{State: "running", Phase: phase, UpdatedAt: time.Now().UTC().Format(time.RFC3339)})
	}
	defer func() {
		if result != nil {
			result = errors.Join(result, persistPreparationStatus(statePath, preparationFailure(phase, result, time.Now())))
		}
	}()
	if err := stage(phase); err != nil {
		return err
	}
	log.Printf("vegad: importando chaves de assinatura confiáveis")
	if err := importKeys(); err != nil {
		return err
	}
	if err := stage("refreshing"); err != nil {
		return err
	}
	log.Printf("vegad: atualizando metadados dos repositórios")
	if err := packages.SyncDatabase(); err != nil {
		var key *distro.UntrustedKeyError
		if errors.As(err, &key) {
			if discoverer, ok := packages.(interface{ DiscoverPreparationKey() error }); ok {
				return discoverer.DiscoverPreparationKey()
			}
		}
		return err
	}
	// List from the refreshed metadata; do not refresh again or wait for
	// Flatpak while this service is blocking native transactions.
	if err := stage("listing-updates"); err != nil {
		return err
	}
	updates, err := packages.ListUpdates()
	if err != nil {
		return err
	}
	if err := stage("publishing"); err != nil {
		return err
	}
	previous, _ := readUpdateStatus(updateStatePath())
	if err := publish(initialRepositoryStatus(activeProfile, updates, previous)); err != nil {
		return err
	}
	if err := writeFirstUpdateMarker(marker); err != nil {
		return fmt.Errorf("registrar preparação dos repositórios: %w", err)
	}
	if err := persistPreparationStatus(statePath, PreparationStatus{State: "completed", Phase: "completed", UpdatedAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		return err
	}
	log.Printf("vegad: preparação dos repositórios concluída")
	return nil
}
