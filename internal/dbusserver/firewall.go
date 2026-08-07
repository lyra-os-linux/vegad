package dbusserver

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

// FirewallService backs org.lyraos.Vega1.Firewall: orchestrates firewalld,
// exposing friendly service names instead of raw port numbers.
type FirewallService struct {
	activity *Activity
	conn     *dbus.Conn
}

type FirewallServiceInfo struct {
	Name    string // firewalld service id ("samba")
	Label   string // friendly label, e.g. "Compartilhamento de arquivos"
	Enabled bool
}

func (f *FirewallService) Status() (bool, string, *dbus.Error) {
	f.activity.Touch()
	if commandAvailable("firewall-cmd") {
		return firewalldStatus()
	}
	return false, "", dbus.MakeFailedError(fmt.Errorf("firewalld não está disponível"))
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
	return dbus.MakeFailedError(fmt.Errorf("firewalld não está disponível"))
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

// FirewallPortInfo backs the (port, protocol) pairs of ListPorts/AddPort/
// RemovePort — "port" is a single number ("8080") or a range ("9000-9010").
type FirewallPortInfo struct {
	Port     string
	Protocol string
}

func (f *FirewallService) ListPorts() ([]FirewallPortInfo, *dbus.Error) {
	f.activity.Touch()

	if !commandAvailable("firewall-cmd") {
		return []FirewallPortInfo{}, nil
	}

	out, err := runCommandOutput("firewall-cmd", "--list-ports")
	if err != nil {
		return nil, dbus.MakeFailedError(fmt.Errorf("firewall-cmd --list-ports: %w — %s", err, out))
	}

	var ports []FirewallPortInfo
	for _, token := range strings.Fields(out) {
		port, protocol, ok := strings.Cut(token, "/")
		if !ok {
			continue
		}
		ports = append(ports, FirewallPortInfo{Port: port, Protocol: protocol})
	}
	return ports, nil
}

func (f *FirewallService) AddPort(sender dbus.Sender, port string, protocol string) *dbus.Error {
	f.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.firewall.configure"); err != nil {
		return err
	}
	protocol, err := normalizePortRule(port, protocol)
	if err != nil {
		return dbus.MakeFailedError(err)
	}
	if !commandAvailable("firewall-cmd") {
		return dbus.MakeFailedError(fmt.Errorf("firewalld não está disponível"))
	}
	return firewalldSetPort(port, protocol, true)
}

func (f *FirewallService) RemovePort(sender dbus.Sender, port string, protocol string) *dbus.Error {
	f.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.firewall.configure"); err != nil {
		return err
	}
	protocol, err := normalizePortRule(port, protocol)
	if err != nil {
		return dbus.MakeFailedError(err)
	}
	if !commandAvailable("firewall-cmd") {
		return dbus.MakeFailedError(fmt.Errorf("firewalld não está disponível"))
	}
	return firewalldSetPort(port, protocol, false)
}

func firewalldSetPort(port string, protocol string, add bool) *dbus.Error {
	action := "--remove-port"
	if add {
		action = "--add-port"
	}

	spec := fmt.Sprintf("%s/%s", port, protocol)
	if err := runCommand("firewall-cmd", "--permanent", action+"="+spec); err != nil {
		return dbus.MakeFailedError(fmt.Errorf("firewall-cmd: %w", err))
	}
	if err := runCommand("firewall-cmd", "--reload"); err != nil {
		return dbus.MakeFailedError(fmt.Errorf("firewall-cmd --reload: %w", err))
	}
	return nil
}

// normalizePortRule validates port (a single 1-65535 number or a
// "start-end" range within that bound, start <= end) and protocol (tcp or
// udp, case-insensitive), returning the normalized lowercase protocol.
func normalizePortRule(port string, protocol string) (string, error) {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol != "tcp" && protocol != "udp" {
		return "", fmt.Errorf("protocolo inválido (use tcp ou udp): %q", protocol)
	}

	start, end, ok := strings.Cut(port, "-")
	if !ok {
		if !validPortNumber(start) {
			return "", fmt.Errorf("porta inválida: %q", port)
		}
		return protocol, nil
	}
	if !validPortNumber(start) || !validPortNumber(end) {
		return "", fmt.Errorf("intervalo de portas inválido: %q", port)
	}
	startNum, _ := strconv.Atoi(start)
	endNum, _ := strconv.Atoi(end)
	if startNum > endNum {
		return "", fmt.Errorf("intervalo de portas inválido (início maior que fim): %q", port)
	}
	return protocol, nil
}

func validPortNumber(value string) bool {
	number, err := strconv.Atoi(value)
	return err == nil && number >= 1 && number <= 65535
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
