package dbusserver

import (
	"fmt"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"
)

// FirewallService backs org.lyraos.Vega1.Firewall: orchestrates whichever
// firewall manager is present — firewalld (Arch/openSUSE) or ufw
// (Debian/Ubuntu, see ufw.go) — exposing friendly service names instead of
// raw port numbers. Dispatch is by tool presence, not by distro ID, same
// pattern SnapshotsService used to use for snapper/Timeshift before the
// Timeshift backend was dropped (see snapshots.go).
type FirewallService struct {
	activity *Activity
	conn     *dbus.Conn
}

type FirewallServiceInfo struct {
	Name    string // firewalld service id ("samba") or ufw app name ("Samba")
	Label   string // friendly label, e.g. "Compartilhamento de arquivos"
	Enabled bool
}

func (f *FirewallService) Status() (bool, string, *dbus.Error) {
	f.activity.Touch()
	if commandAvailable("firewall-cmd") {
		return firewalldStatus()
	}
	if ufwInstalled() {
		active, zone := ufwStatus()
		return active, zone, nil
	}
	return false, "", dbus.MakeFailedError(fmt.Errorf("nenhum firewall gerenciável (firewalld ou ufw) está disponível"))
}

func firewalldStatus() (bool, string, *dbus.Error) {
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

var firewalldCatalog = []struct {
	name  string
	label string
}{
	{name: "ssh", label: "Acesso remoto (SSH)"},
	{name: "samba", label: "Compartilhamento de arquivos"},
	{name: "mdns", label: "Descoberta na rede"},
	{name: "dhcpv6-client", label: "Cliente DHCPv6"},
	{name: "cockpit", label: "Painel Cockpit"},
	{name: "ipp", label: "Impressoras"},
}

func (f *FirewallService) ListServices() ([]FirewallServiceInfo, *dbus.Error) {
	f.activity.Touch()

	if commandAvailable("firewall-cmd") {
		return firewalldListServices(), nil
	}
	if ufwInstalled() {
		return ufwListServices(), nil
	}
	return []FirewallServiceInfo{}, nil
}

func firewalldListServices() []FirewallServiceInfo {
	enabled := map[string]bool{}
	if out, err := runCommandOutput("firewall-cmd", "--list-services"); err == nil {
		for _, service := range strings.Fields(out) {
			enabled[service] = true
		}
	}

	var rows []FirewallServiceInfo
	seen := map[string]bool{}
	for _, item := range firewalldCatalog {
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
	return rows
}

func (f *FirewallService) SetServiceEnabled(sender dbus.Sender, name string, enabled bool) *dbus.Error {
	f.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.firewall.configure"); err != nil {
		return err
	}

	if commandAvailable("firewall-cmd") {
		return firewalldSetServiceEnabled(name, enabled)
	}
	if ufwInstalled() {
		if err := ufwSetServiceEnabled(name, enabled); err != nil {
			return dbus.MakeFailedError(fmt.Errorf("ufw: %w", err))
		}
		return nil
	}
	return dbus.MakeFailedError(fmt.Errorf("nenhum firewall gerenciável (firewalld ou ufw) está disponível"))
}

func firewalldSetServiceEnabled(name string, enabled bool) *dbus.Error {
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
