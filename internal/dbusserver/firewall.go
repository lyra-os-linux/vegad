package dbusserver

import (
	"fmt"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"
)

// FirewallService backs org.lyraos.Vega1.Firewall (PROMPT-VEGA.md §3.5):
// orchestrates firewalld, exposing friendly service names instead of raw
// port numbers.
type FirewallService struct {
	activity *Activity
	conn     *dbus.Conn
}

type FirewallServiceInfo struct {
	Name    string // firewalld service id, e.g. "samba"
	Label   string // friendly label, e.g. "Compartilhamento de arquivos"
	Enabled bool
}

func (f *FirewallService) Status() (bool, string, *dbus.Error) {
	f.activity.Touch()
	if !commandAvailable("firewall-cmd") {
		return false, "", dbus.MakeFailedError(fmt.Errorf("firewalld não está disponível"))
	}

	out, err := runCommandOutput("firewall-cmd", "--state")
	if err != nil {
		if strings.Contains(strings.ToLower(out), "not running") {
			return false, "", nil
		}
		return false, "", dbus.MakeFailedError(fmt.Errorf("firewall-cmd --state: %w — %s", err, out))
	}

	zone := ""
	if strings.TrimSpace(out) == "running" {
		if zoneOut, zoneErr := runCommandOutput("firewall-cmd", "--get-active-zone"); zoneErr == nil {
			zone = firstActiveZone(zoneOut)
		}
	}
	return strings.TrimSpace(out) == "running", zone, nil
}

func (f *FirewallService) ListServices() ([]FirewallServiceInfo, *dbus.Error) {
	f.activity.Touch()
	enabled := map[string]bool{}
	if !commandAvailable("firewall-cmd") {
		return []FirewallServiceInfo{}, nil
	}
	if out, err := runCommandOutput("firewall-cmd", "--list-services"); err == nil {
		for _, service := range strings.Fields(out) {
			enabled[service] = true
		}
	}

	catalog := []struct {
		name  string
		label string
	}{
		{name: "ssh", label: "Acesso remoto (SSH)"},
		{name: "samba", label: "Compartilhamento de arquivos"},
		{name: "mdns", label: "Descoberta na rede"},
		{name: "dhcpv6-client", label: "Cliente DHCPv6"},
		{name: "cockpit", label: "Painel Cockpit"},
		{name: "printer", label: "Impressoras"},
	}

	var rows []FirewallServiceInfo
	seen := map[string]bool{}
	for _, item := range catalog {
		rows = append(rows, FirewallServiceInfo{
			Name:    item.name,
			Label:   item.label,
			Enabled: enabled[item.name],
		})
		seen[item.name] = true
	}
	for name := range enabled {
		if seen[name] {
			continue
		}
		rows = append(rows, FirewallServiceInfo{
			Name:    name,
			Label:   name,
			Enabled: true,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

func (f *FirewallService) SetServiceEnabled(sender dbus.Sender, name string, enabled bool) *dbus.Error {
	f.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.firewall.configure"); err != nil {
		return err
	}
	if !commandAvailable("firewall-cmd") {
		return dbus.MakeFailedError(fmt.Errorf("firewalld não está disponível"))
	}

	action := "--remove-service"
	if enabled {
		action = "--add-service"
	}

	if err := runCommand("firewall-cmd", "--permanent", action, name); err != nil {
		return dbus.MakeFailedError(fmt.Errorf("firewall-cmd: %w", err))
	}
	if err := runCommand("firewall-cmd", "--reload"); err != nil {
		return dbus.MakeFailedError(fmt.Errorf("firewall-cmd --reload: %w", err))
	}
	return nil
}

func firstActiveZone(value string) string {
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if idx := strings.Index(line, "("); idx > 0 {
			return strings.TrimSpace(line[:idx])
		}
		return line
	}
	return ""
}
