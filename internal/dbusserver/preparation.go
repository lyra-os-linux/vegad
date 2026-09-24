package dbusserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/distro"
	"github.com/lyraos/vegad/internal/profile"
)

// PreparationStatus has a separate wire contract from update counts. Error is
// a safe user-facing summary; command output remains in the privileged journal.
type PreparationStatus struct {
	State       string `json:"state"`
	Phase       string `json:"phase"`
	ErrorKind   string `json:"errorKind"`
	LastError   string `json:"lastError"`
	UpdatedAt   string `json:"updatedAt"`
	NextRetryAt string `json:"nextRetryAt"`
	CanRetry    bool   `json:"canRetry"`
}

func preparationStatePath(marker string) string {
	return filepath.Join(filepath.Dir(marker), "first-update-status.json")
}

func persistPreparationStatus(path string, status PreparationStatus) error {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	// The status contains only safe summaries and must be readable by the
	// unprivileged query worker, like update-status.json.
	if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
		return err
	}
	// Unique temporary files avoid collisions with readers and concurrent jobs.
	file, err := os.CreateTemp(filepath.Dir(path), ".preparation-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err = file.Chmod(0644); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func readPreparationStatus(path string) (PreparationStatus, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return PreparationStatus{State: "pending"}, nil
	}
	if err != nil {
		return PreparationStatus{}, err
	}
	var status PreparationStatus
	err = json.Unmarshal(data, &status)
	return status, err
}

func preparationFailure(phase string, err error, now time.Time) PreparationStatus {
	status := PreparationStatus{State: "failed", Phase: phase, ErrorKind: "operation-failed", LastError: "Não foi possível concluir a preparação dos repositórios. Consulte o registro do serviço.", UpdatedAt: now.UTC().Format(time.RFC3339)}
	var key *distro.UntrustedKeyError
	var refresh *distro.RepositoryRefreshError
	if errors.Is(err, context.Canceled) {
		status.ErrorKind = "interrupted"
		status.LastError = "A preparação foi interrompida. Será retomada na próxima tentativa."
		return status
	} else if errors.As(err, &key) {
		status.State = "awaiting-approval"
		status.ErrorKind = "untrusted-key"
		status.LastError = "Um repositório exige aprovação de uma chave de assinatura. Aguardar não autoriza essa chave."
	} else if errors.As(err, &refresh) && refresh.Kind == "network" {
		status.ErrorKind = "network"
		status.LastError = "Não foi possível acessar os repositórios. Verifique a conexão de rede."
	}
	// This is an estimate of RestartSec, exposed only if systemd confirms that
	// a restart is actually scheduled. It is not a promise of successful recovery.
	status.NextRetryAt = now.Add(15 * time.Minute).UTC().Format(time.RFC3339)
	return status
}

type preparationUnit struct{ Active, Sub, Load string }

func preparationUnitState() (preparationUnit, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show", firstUpdateUnit, "--property=ActiveState,SubState,LoadState").Output()
	if err != nil {
		return preparationUnit{}, err
	}
	unit := preparationUnit{}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "ActiveState":
			unit.Active = value
		case "SubState":
			unit.Sub = value
		case "LoadState":
			unit.Load = value
		}
	}
	return unit, nil
}

func reconcilePreparationStatus(status PreparationStatus, unit preparationUnit) PreparationStatus {
	status.CanRetry = false
	if status.State == "completed" || status.State == "skipped" || status.State == "unavailable" {
		status.NextRetryAt = ""
		return status
	}
	if unit.Load != "loaded" {
		status.State = "unavailable"
		status.NextRetryAt = ""
		return status
	}
	switch {
	case unit.Sub == "auto-restart" || unit.Sub == "auto-restart-queued":
		if status.State != "awaiting-approval" {
			status.State = "waiting-retry"
		}
		status.CanRetry = true
	case unit.Active == "active" || unit.Active == "activating" || unit.Active == "deactivating":
		status.State = "running"
		status.NextRetryAt = ""
	default:
		status.CanRetry = true
		status.NextRetryAt = ""
		if unit.Active == "failed" && status.State == "pending" {
			status.State = "failed"
			status.ErrorKind = "service-failed"
			status.LastError = "O serviço de preparação falhou antes de registrar o diagnóstico. Consulte o registro do serviço."
		}
		if status.State == "waiting-retry" {
			status.State = "failed"
		}
		if status.State == "running" {
			status.State = "failed"
			status.ErrorKind = "interrupted"
			status.LastError = "A preparação foi interrompida antes de concluir. Você pode tentar novamente."
		}
	}
	return status
}

func preparationEligible(activeProfile profile.Profile) bool {
	if activeProfile != profile.Desktop {
		return false
	}
	if _, err := os.Stat("/usr/lib/lyra-os/release"); err != nil {
		return false
	}
	cmdline, err := os.ReadFile("/proc/cmdline")
	return err == nil && !isLiveCmdline(string(cmdline))
}

type PreparationService struct {
	software *SoftwareService
	activity *Activity
	profile  profile.Profile
}

// GetStatus is side-effect free and can be polled even before a user attempts
// a package transaction. Completed/exempted markers remain authoritative.
func (s *PreparationService) GetStatus() (PreparationStatus, *dbus.Error) {
	s.activity.Touch()
	if !preparationEligible(s.profile) {
		return PreparationStatus{State: "unavailable"}, nil
	}
	status := PreparationStatus{}
	for _, marker := range []struct{ path, state string }{{firstUpdateSkipPath(), "skipped"}, {firstUpdateMarkerPath(), "completed"}} {
		if _, err := os.Stat(marker.path); err == nil {
			status, _ = readPreparationStatus(preparationStatePath(firstUpdateMarkerPath()))
			status.State = marker.state
			status.Phase = marker.state
			status.ErrorKind = ""
			status.LastError = ""
			status.NextRetryAt = ""
			status.CanRetry = false
			return status, nil
		} else if !os.IsNotExist(err) {
			return PreparationStatus{}, dbus.MakeFailedError(err)
		}
	}
	status, err := readPreparationStatus(preparationStatePath(firstUpdateMarkerPath()))
	if err != nil {
		return PreparationStatus{}, dbus.MakeFailedError(err)
	}
	if status.State == "completed" || status.State == "skipped" {
		status = PreparationStatus{State: "pending"}
	}
	unit, err := preparationUnitState()
	if err != nil {
		return PreparationStatus{}, dbus.MakeFailedError(fmt.Errorf("consultar serviço de preparação: %w", err))
	}
	return reconcilePreparationStatus(status, unit), nil
}

// Retry never removes completion/exemption markers or interrupts running work.
// Resetting the failed state permits recovery after StartLimitBurst is reached.
func (s *PreparationService) Retry(sender dbus.Sender) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.software.manage-repos"); err != nil {
		return err
	}
	status, err := s.GetStatus()
	if err != nil {
		return err
	}
	if !status.CanRetry {
		return dbus.NewError(BusName+".Error.NotAvailable", []interface{}{"A preparação não pode ser reiniciada neste estado."})
	}
	unit, unitErr := preparationUnitState()
	if unitErr != nil {
		return dbus.MakeFailedError(unitErr)
	}
	if err := retryPreparation(unit.Active == "failed", func(args ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl %s: %w — %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func retryPreparation(resetFailed bool, run func(...string) error) error {
	// Inactive units may be garbage-collected after a status query. Resetting
	// such a unit fails with "not loaded"; only failed units need this step.
	if resetFailed {
		if err := run("reset-failed", firstUpdateUnit); err != nil {
			return err
		}
	}
	// Starting is idempotent if an automatic retry won the race after GetStatus.
	// Never use restart here: it could terminate that newly running process.
	return run("--no-block", "start", firstUpdateUnit)
}

// GetPendingKeys is persistent and read-only; no transient signal is needed.
func (s *PreparationService) GetPendingKeys() ([]distro.PreparationKey, *dbus.Error) {
	status, err := s.GetStatus()
	if err != nil {
		return nil, err
	}
	if status.State != "awaiting-approval" {
		return []distro.PreparationKey{}, nil
	}
	keys, readErr := distro.ReadPreparationKey()
	if readErr != nil {
		return nil, dbus.MakeFailedError(readErr)
	}
	return keys, nil
}

// ApproveKey uses the normal software transaction stream and the same strict
// key prompt responder as AddRepo. The review token prevents stale UI approval.
func (s *PreparationService) ApproveKey(sender dbus.Sender, repo, fingerprint, token string) (uint32, *dbus.Error) {
	s.activity.Touch()
	if err := requireFirstUpdateIdle(); err != nil {
		return 0, err
	}
	if err := requirePolkit(sender, "org.lyraos.vega.software.manage-repos"); err != nil {
		return 0, err
	}
	status, err := s.GetStatus()
	if err != nil {
		return 0, err
	}
	if status.State != "awaiting-approval" || !status.CanRetry {
		return 0, dbus.MakeFailedError(fmt.Errorf("nenhuma chave aguardando aprovação"))
	}
	if s.software == nil {
		return 0, dbus.MakeFailedError(fmt.Errorf("serviço de software indisponível"))
	}
	backend, ok := s.software.provider.Package().(interface {
		TrustPreparationKey(string, string, string, distro.ProgressFunc) error
	})
	if !ok {
		return 0, dbus.MakeFailedError(fmt.Errorf("aprovação indisponível neste backend"))
	}
	return s.software.startTransaction("Aprovar chave da preparação: "+repo, func(report progressFunc, _ packageProgressFunc) error {
		if err := backend.TrustPreparationKey(repo, fingerprint, token, report); err != nil {
			return err
		}
		unit, err := preparationUnitState()
		if err != nil {
			return err
		}
		return retryPreparation(unit.Active == "failed", func(args ...string) error {
			out, err := exec.Command("systemctl", args...).CombinedOutput()
			if err != nil {
				return fmt.Errorf("retomar preparação: %w: %s", err, out)
			}
			return nil
		})
	}), nil
}
