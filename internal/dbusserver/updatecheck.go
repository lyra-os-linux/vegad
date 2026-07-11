package dbusserver

import (
	"fmt"
	"log"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/distro"
)

// RunUpdateCheckJob lists pending package-manager/Flatpak updates and, if
// any are found, emits UpdatesAvailable on the system bus. It is invoked
// directly by vegad-update-check.service (see cmd/vegad), not through the
// bus-activated Server, so it works on its own systemd timer schedule
// regardless of whether the main daemon is currently running.
func RunUpdateCheckJob() error {
	id, err := distro.Detect()
	if err != nil {
		return err
	}
	provider, err := distro.NewProvider(id)
	if err != nil {
		return err
	}

	if err := provider.Package().SyncDatabase(); err != nil {
		return err
	}

	official, err := provider.Package().ListUpdates()
	if err != nil {
		return err
	}
	flathub, err := listFlatpakUpdates()
	if err != nil {
		return err
	}

	count := len(official) + len(flathub)
	if count == 0 {
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

	log.Printf("vegad: %d atualizações pendentes, sinal UpdatesAvailable emitido", count)
	return nil
}
