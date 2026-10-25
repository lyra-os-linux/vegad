package dbusserver

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	running, err := firewalldRunning()
	if err != nil {
		return false, "", dbus.MakeFailedError(err)
	}
	if !running {
		return false, "", nil
	}
	zone := ""
	if zoneOut, zoneErr := runCommandOutput("firewall-cmd", "--get-active-zone"); zoneErr == nil {
		zone = firstActiveZone(zoneOut)
	} else {
		log.Printf("vegad: firewall active zone unavailable: %v — %s", zoneErr, strings.TrimSpace(zoneOut))
	}
	return true, zone, nil
}

// firewalldRunning asks systemd whether firewalld is active instead of running
// `firewall-cmd --state`. That command talks to firewalld over D-Bus and needs
// a polkit authorization that only an active local session receives silently;
// from SSH or the vega-web broker it fails with "Authorization failed"
// (vegad#79). `systemctl is-active` needs no authorization and runs as the
// ordinary caller. The active zone query does need it: Status runs in the
// broker, as root, so that firewall-cmd can read the runtime zones.
func firewalldRunning() (bool, error) {
	out, err := runCommandOutput("systemctl", "is-active", "firewalld.service")
	switch strings.TrimSpace(out) {
	case "active", "reloading", "refreshing", "activating":
		return true, nil
	case "inactive", "failed", "deactivating":
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("systemctl is-active firewalld: %w — %s", err, out)
	}
	return false, nil
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

// netInterfaceExists reports whether a network interface is present. firewalld
// ships predefined zones bound to interfaces that may not exist (the "docker"
// zone lists docker0 even without Docker), and those still show up in
// --get-active-zone. Replaceable in tests.
var netInterfaceExists = func(name string) bool {
	_, err := os.Stat(filepath.Join("/sys/class/net", name))
	return err == nil
}

type activeZone struct {
	name      string
	isDefault bool
	bound     bool // has an existing interface or a source
}

// firstActiveZone picks the zone that actually filters this machine's traffic
// from `firewall-cmd --get-active-zone` output (vegad#83): the default zone
// when it is bound to an existing interface or a source, otherwise the first
// bound zone, otherwise the default zone, otherwise the first zone listed.
func firstActiveZone(value string) string {
	var zones []activeZone
	for _, raw := range strings.Split(value, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		indented := raw != strings.TrimLeft(raw, " \t")
		if !indented || len(zones) == 0 {
			zone := activeZone{name: line}
			if idx := strings.Index(line, "("); idx > 0 {
				zone.name = strings.TrimSpace(line[:idx])
				zone.isDefault = strings.Contains(line[idx:], "default")
			}
			zones = append(zones, zone)
			continue
		}
		current := &zones[len(zones)-1]
		key, values, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		for _, item := range strings.Fields(values) {
			switch strings.TrimSpace(key) {
			case "interfaces":
				if netInterfaceExists(item) {
					current.bound = true
				}
			case "sources":
				current.bound = true
			}
		}
	}
	if len(zones) == 0 {
		return ""
	}
	for _, zone := range zones {
		if zone.isDefault && zone.bound {
			return zone.name
		}
	}
	for _, zone := range zones {
		if zone.bound {
			return zone.name
		}
	}
	for _, zone := range zones {
		if zone.isDefault {
			return zone.name
		}
	}
	return zones[0].name
}
