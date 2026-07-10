package dbusserver

import (
	"fmt"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"
)

// ServicesService backs org.lyraos.Vega1.Services (PROMPT-VEGA.md §3.7):
// a curated list of user-facing systemd units with enable/disable and
// start/stop controls.
type ServicesService struct {
	activity *Activity
}

type ManagedServiceInfo struct {
	Name        string
	Label       string
	Description string
	Enabled     bool
	Active      bool
	Available   bool
}

var curatedServices = []struct {
	name        string
	label       string
	description string
}{
	{name: "sshd.service", label: "Acesso remoto", description: "Servidor SSH"},
	{name: "bluetooth.service", label: "Bluetooth", description: "Gerenciador do Bluetooth"},
	{name: "cups.service", label: "Impressão", description: "Sistema de impressão"},
	{name: "NetworkManager.service", label: "Rede", description: "Gerenciador de conexões"},
	{name: "firewalld.service", label: "Firewall", description: "Firewall do sistema"},
	{name: "avahi-daemon.service", label: "Descoberta na rede", description: "Serviço mDNS/Bonjour"},
}

func (s *ServicesService) ListServices() ([]ManagedServiceInfo, *dbus.Error) {
	s.activity.Touch()

	var rows []ManagedServiceInfo
	for _, item := range curatedServices {
		rows = append(rows, serviceInfo(item.name, item.label, item.description))
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Label < rows[j].Label })
	return rows, nil
}

func (s *ServicesService) SetServiceEnabled(sender dbus.Sender, name string, enabled bool) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.services.configure"); err != nil {
		return err
	}
	if err := setServiceEnabled(name, enabled); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (s *ServicesService) SetServiceRunning(sender dbus.Sender, name string, running bool) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.services.configure"); err != nil {
		return err
	}
	if err := setServiceRunning(name, running); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (s *ServicesService) RestartService(sender dbus.Sender, name string) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.services.configure"); err != nil {
		return err
	}
	if err := restartService(name); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func serviceInfo(name, label, description string) ManagedServiceInfo {
	enabled := false
	active := false
	available := false

	if commandAvailable("systemctl") {
		if out, err := runCommandOutput("systemctl", "is-enabled", name); err == nil {
			enabled = strings.TrimSpace(out) == "enabled" || strings.TrimSpace(out) == "static"
		}
		if out, err := runCommandOutput("systemctl", "is-active", name); err == nil {
			active = strings.TrimSpace(out) == "active"
		}
		if out, err := runCommandOutput("systemctl", "show", "-p", "LoadState", "--value", name); err == nil {
			available = strings.TrimSpace(out) != "" && strings.TrimSpace(out) != "not-found"
		}
	}

	return ManagedServiceInfo{
		Name:        name,
		Label:       label,
		Description: description,
		Enabled:     enabled,
		Active:      active,
		Available:   available,
	}
}

func setServiceEnabled(name string, enabled bool) error {
	if !commandAvailable("systemctl") {
		return fmt.Errorf("systemctl não está disponível")
	}

	args := []string{}
	if enabled {
		args = append(args, "enable", "--now", name)
	} else {
		args = append(args, "disable", "--now", name)
	}
	if out, err := runCommandOutput("systemctl", args...); err != nil {
		if enabled {
			// Some units are static or instantiated; if enable failed, keep the
			// explicit error because the user asked for a persistent change.
			return fmt.Errorf("systemctl %s: %w — %s", strings.Join(args, " "), err, out)
		}
		// Disabling an already-disabled or unavailable unit is not fatal for the
		// UI surface; we still want the running state to be adjusted below.
	}
	return nil
}

func setServiceRunning(name string, running bool) error {
	if !commandAvailable("systemctl") {
		return fmt.Errorf("systemctl não está disponível")
	}

	action := "stop"
	if running {
		action = "start"
	}
	if out, err := runCommandOutput("systemctl", action, name); err != nil {
		return fmt.Errorf("systemctl %s %s: %w — %s", action, name, err, out)
	}
	return nil
}

func restartService(name string) error {
	if !commandAvailable("systemctl") {
		return fmt.Errorf("systemctl não está disponível")
	}
	if out, err := runCommandOutput("systemctl", "restart", name); err != nil {
		return fmt.Errorf("systemctl restart %s: %w — %s", name, err, out)
	}
	return nil
}
