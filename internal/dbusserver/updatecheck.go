package dbusserver

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/distro"
	"github.com/lyraos/vegad/internal/profile"
)

const defaultUpdateStatePath = "/var/lib/vega/update-status.json"

type UpdateStatus struct {
	CheckedAt     string `json:"checkedAt"`
	Profile       string `json:"profile"`
	NativeCount   uint32 `json:"nativeCount"`
	FlatpakCount  uint32 `json:"flatpakCount"`
	TotalCount    uint32 `json:"totalCount"`
	SecurityCount uint32 `json:"securityCount"`
	InProgress    bool   `json:"inProgress"`
	Error         string `json:"error,omitempty"`
}

func updateStatePath() string {
	if path := os.Getenv("VEGAD_UPDATE_STATE"); path != "" {
		return path
	}
	return defaultUpdateStatePath
}

func persistUpdateStatus(path string, status UpdateStatus) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readUpdateStatus(path string) (UpdateStatus, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return UpdateStatus{}, err
	}
	var status UpdateStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return UpdateStatus{}, err
	}
	return status, nil
}

func updateStatusChanged(previous, current UpdateStatus) bool {
	return previous.Profile != current.Profile ||
		previous.NativeCount != current.NativeCount ||
		previous.FlatpakCount != current.FlatpakCount ||
		previous.TotalCount != current.TotalCount ||
		previous.SecurityCount != current.SecurityCount ||
		previous.InProgress != current.InProgress ||
		previous.Error != current.Error
}

func updateResultChanged(previous, current UpdateStatus) bool {
	return previous.Profile != current.Profile ||
		previous.NativeCount != current.NativeCount ||
		previous.FlatpakCount != current.FlatpakCount ||
		previous.TotalCount != current.TotalCount ||
		previous.SecurityCount != current.SecurityCount ||
		previous.Error != current.Error
}

// RunUpdateCheckJob lists pending package-manager/Flatpak updates, persists
// the result and emits UpdatesAvailable when the count changes. It is invoked
// directly by vegad-update-check.service (see cmd/vegad), not through the
// bus-activated Server, so it works on its own systemd timer schedule
// regardless of whether the main daemon is currently running.
func RunUpdateCheckJob(activeProfile profile.Profile) error {
	status := UpdateStatus{CheckedAt: time.Now().UTC().Format(time.RFC3339), Profile: string(activeProfile)}
	id, err := distro.Detect()
	if err != nil {
		status.Error = err.Error()
		_ = persistUpdateStatus(updateStatePath(), status)
		return err
	}
	provider, err := distro.NewProvider(id)
	if err != nil {
		return err
	}

	if err := provider.Package().SyncDatabase(); err != nil {
		status.Error = err.Error()
		_ = persistUpdateStatus(updateStatePath(), status)
		return err
	}

	official, err := provider.Package().ListUpdates()
	if err != nil {
		status.Error = err.Error()
		_ = persistUpdateStatus(updateStatePath(), status)
		return err
	}
	// Runs as its own short-lived process on a systemd timer, with no D-Bus
	// caller to resolve a desktop user from — only the system-wide Flatpak
	// installation is checked here (see listFlatpakUpdates).
	var flathub []PackageRef
	if activeProfile == profile.Desktop {
		flathub, err = listFlatpakUpdates(nil)
		if err != nil {
			status.Error = err.Error()
			_ = persistUpdateStatus(updateStatePath(), status)
			return err
		}
	}

	count := len(official) + len(flathub)
	status.NativeCount = uint32(len(official))
	status.FlatpakCount = uint32(len(flathub))
	status.TotalCount = uint32(count)
	previous, previousErr := readUpdateStatus(updateStatePath())
	if err := persistUpdateStatus(updateStatePath(), status); err != nil {
		return fmt.Errorf("persistir estado de atualizações: %w", err)
	}
	if previousErr == nil && !updateResultChanged(previous, status) {
		log.Printf("vegad: estado de atualizações inalterado (%d pendentes)", count)
		return nil
	}

	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("conectar ao barramento de sistema: %w", err)
	}
	defer conn.Close()

	if err := conn.Emit(ObjectPath, BusName+".Software.UpdatesAvailable", uint32(count)); err != nil {
		return fmt.Errorf("emitir UpdatesAvailable: %w", err)
	}
	if err := conn.Emit(ObjectPath, BusName+".Software.UpdateStateChanged", status); err != nil {
		return fmt.Errorf("emitir UpdateStateChanged: %w", err)
	}

	log.Printf("vegad: estado alterado para %d atualizações pendentes, sinal UpdatesAvailable emitido", count)
	return nil
}
