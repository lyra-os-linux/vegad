package dbusserver

import (
	"fmt"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"
	vegai18n "github.com/lyraos/vegad/internal/i18n"
)

// ServicesService backs org.lyraos.Vega1.Services: a curated list of user-facing systemd units with enable/disable and
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
	name           string
	labelKey       string
	descriptionKey string
}{
	{name: "sshd.service", labelKey: "services.remote_access.label", descriptionKey: "services.remote_access.description"},
	{name: "bluetooth.service", labelKey: "Bluetooth", descriptionKey: "services.bluetooth.description"},
	{name: "cups.service", labelKey: "services.printing.label", descriptionKey: "services.printing.description"},
	{name: "NetworkManager.service", labelKey: "services.network.label", descriptionKey: "services.network.description"},
	{name: "firewalld.service", labelKey: "Firewall", descriptionKey: "services.firewall.description"},
	{name: "avahi-daemon.service", labelKey: "services.discovery.label", descriptionKey: "services.discovery.description"},
}

func (s *ServicesService) ListServices() ([]ManagedServiceInfo, *dbus.Error) {
	return s.ListServicesLocalized(vegai18n.DefaultLocale)
}

func (s *ServicesService) ListServicesLocalized(locale string) ([]ManagedServiceInfo, *dbus.Error) {
	s.activity.Touch()

	var rows []ManagedServiceInfo
	for _, item := range curatedServices {
		rows = append(rows, serviceInfo(item.name, vegai18n.T(locale, item.labelKey), vegai18n.T(locale, item.descriptionKey)))
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Label < rows[j].Label })
	return rows, nil
}

func (s *ServicesService) ListAllServices() ([]ManagedServiceInfo, *dbus.Error) {
	return s.ListAllServicesLocalized(vegai18n.DefaultLocale)
}

func (s *ServicesService) ListAllServicesLocalized(locale string) ([]ManagedServiceInfo, *dbus.Error) {
	s.activity.Touch()
	rows, err := listAllSystemdServices(locale)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
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

func listAllSystemdServices(locale string) ([]ManagedServiceInfo, error) {
	if !commandAvailable("systemctl") {
		return nil, fmt.Errorf("systemctl não está disponível")
	}

	enabledByUnit := map[string]bool{}
	availableByUnit := map[string]bool{}
	unitFiles, err := runCommandOutput("systemctl", "list-unit-files", "--type=service", "--no-legend", "--no-pager")
	if err != nil {
		return nil, fmt.Errorf("systemctl list-unit-files: %w — %s", err, unitFiles)
	}
	for _, line := range strings.Split(unitFiles, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasSuffix(fields[0], ".service") {
			continue
		}
		name := fields[0]
		state := fields[1]
		availableByUnit[name] = true
		enabledByUnit[name] = state == "enabled" || state == "enabled-runtime" || state == "static" || state == "alias"
	}

	activeByUnit := map[string]bool{}
	descriptionByUnit := map[string]string{}
	units, err := runCommandOutput("systemctl", "list-units", "--type=service", "--all", "--no-legend", "--no-pager")
	if err != nil {
		return nil, fmt.Errorf("systemctl list-units: %w — %s", err, units)
	}
	for _, line := range strings.Split(units, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "●" {
			fields = fields[1:]
		}
		if len(fields) < 4 || !strings.HasSuffix(fields[0], ".service") {
			continue
		}
		name := fields[0]
		availableByUnit[name] = true
		activeByUnit[name] = fields[2] == "active"
		if len(fields) > 4 {
			descriptionByUnit[name] = strings.Join(fields[4:], " ")
		}
	}

	rows := make([]ManagedServiceInfo, 0, len(availableByUnit))
	for name := range availableByUnit {
		description := descriptionByUnit[name]
		if description == "" {
			description = vegai18n.T(locale, "services.systemd.description")
		}
		rows = append(rows, ManagedServiceInfo{
			Name:        name,
			Label:       serviceLabelFromName(name),
			Description: description,
			Enabled:     enabledByUnit[name],
			Active:      activeByUnit[name],
			Available:   true,
		})
	}

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

func serviceLabelFromName(name string) string {
	label := strings.TrimSuffix(name, ".service")
	label = strings.ReplaceAll(label, "-", " ")
	label = strings.ReplaceAll(label, "_", " ")
	if label == "" {
		return name
	}
	return label
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
