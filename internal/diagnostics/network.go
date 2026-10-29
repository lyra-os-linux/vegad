package diagnostics

import (
	"context"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ListeningSocket is one socket of Firewall.ListListening (lyra-ecosystem
// openspec change add-security-review, capability network-exposure).
type ListeningSocket struct {
	Protocol   string
	Address    string
	Port       uint16
	Executable string
	Unit       string
	// Reach is "local", "link" or "network".
	Reach string
	// State is "exposed" or "filtered" for network reach, empty otherwise.
	State string
	// Zone decided State; Allow names what let the port in, as a close-port
	// argument ("service:ssh", "port:8000/tcp") or "target" for a zone that
	// accepts everything. Allow is not part of the wire contract.
	Zone  string
	Allow string
}

var ssProcess = regexp.MustCompile(`\(\("[^"]*",pid=(\d+),`)

type ssEntry struct {
	protocol, address, iface string
	port                     uint16
	pid                      int
}

// parseSS reads `ss -H -tulpn`: netid, state, queues, local address, peer,
// then users:((...)) when the process is visible.
func parseSS(out string) []ssEntry {
	var entries []ssEntry
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		protocol := fields[0]
		if protocol != "tcp" && protocol != "udp" {
			continue
		}
		if protocol == "tcp" && fields[1] != "LISTEN" {
			continue
		}
		local := fields[4]
		colon := strings.LastIndex(local, ":")
		if colon < 0 {
			continue
		}
		port, err := strconv.ParseUint(local[colon+1:], 10, 16)
		if err != nil {
			continue
		}
		address, iface := local[:colon], ""
		if i := strings.LastIndex(address, "%"); i >= 0 {
			address, iface = address[:i], address[i+1:]
		}
		address = strings.Trim(address, "[]")
		entry := ssEntry{protocol: protocol, address: address, iface: iface, port: uint16(port)}
		if m := ssProcess.FindStringSubmatch(line); m != nil {
			entry.pid, _ = strconv.Atoi(m[1])
		}
		entries = append(entries, entry)
	}
	return entries
}

var virtualInterface = regexp.MustCompile(`^(docker|virbr|veth|br-|podman|cni)`)

// reachOf classifies where a bound address can be reached from.
func reachOf(address, iface string, virtualAddresses map[string]bool) string {
	if iface != "" && virtualInterface.MatchString(iface) {
		return "link"
	}
	switch address {
	case "*", "0.0.0.0", "::":
		return "network"
	}
	ip := net.ParseIP(address)
	switch {
	case ip == nil:
		return "network"
	case ip.IsLoopback():
		return "local"
	case ip.IsLinkLocalUnicast():
		return "link"
	case virtualAddresses[ip.String()]:
		return "link"
	}
	return "network"
}

// unitOf returns the systemd service owning pid, from /proc/<pid>/cgroup.
// Scopes (apps and terminals) are not services: a process started from a
// terminal has no unit.
func unitOf(env *Env, pid int) string {
	if pid <= 0 {
		return ""
	}
	data, err := env.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if err != nil {
		return ""
	}
	// Only the process's own cgroup counts: an app scope below
	// user@1001.service belongs to the app, not to the user manager.
	for _, line := range strings.Split(string(data), "\n") {
		path := line[strings.LastIndex(line, ":")+1:]
		if leaf := path[strings.LastIndex(path, "/")+1:]; strings.HasSuffix(leaf, ".service") {
			return leaf
		}
	}
	return ""
}

// firewallZone is one zone of `firewall-cmd --list-all-zones`.
type firewallZone struct {
	name       string
	active     bool
	isDefault  bool
	target     string
	interfaces []string
	sources    []string
	services   []string
	ports      []string
}

func parseFirewallZones(out string) []firewallZone {
	var zones []firewallZone
	var current *firewallZone
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") {
			name, flags, _ := strings.Cut(line, " ")
			zones = append(zones, firewallZone{name: name, active: strings.Contains(flags, "active"), isDefault: strings.Contains(flags, "default")})
			current = &zones[len(zones)-1]
			continue
		}
		if current == nil {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		values := strings.Fields(value)
		switch key {
		case "target":
			current.target = strings.TrimSpace(value)
		case "interfaces":
			current.interfaces = values
		case "sources":
			current.sources = values
		case "services":
			current.services = values
		case "ports":
			current.ports = values
		}
	}
	return zones
}

// parseServicePorts reads the "ports:" line of `firewall-cmd --info-service`.
func parseServicePorts(out string) []string {
	for _, line := range strings.Split(out, "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && key == "ports" {
			return strings.Fields(value)
		}
	}
	return nil
}

// portMatches reports whether "8000/tcp" or "9000-9010/tcp" covers port.
func portMatches(spec string, port uint16, protocol string) bool {
	numbers, proto, ok := strings.Cut(spec, "/")
	if !ok || proto != protocol {
		return false
	}
	low, high, isRange := strings.Cut(numbers, "-")
	from, err := strconv.Atoi(low)
	if err != nil {
		return false
	}
	to := from
	if isRange {
		if to, err = strconv.Atoi(high); err != nil {
			return false
		}
	}
	return int(port) >= from && int(port) <= to
}

// zoneReachesNetwork is false for zones bound only to container or VM
// bridges: what they accept stays inside the machine.
func (z firewallZone) reachesNetwork() bool {
	if len(z.sources) > 0 || (z.isDefault && len(z.interfaces) == 0) {
		return true
	}
	for _, iface := range z.interfaces {
		if !virtualInterface.MatchString(iface) {
			return true
		}
	}
	return false
}

// allowedBy returns what in zone lets protocol/port in, or "".
func (z firewallZone) allowedBy(port uint16, protocol string, servicePorts map[string][]string) string {
	if z.target == "ACCEPT" {
		return "target"
	}
	for _, spec := range z.ports {
		if portMatches(spec, port, protocol) {
			return "port:" + spec
		}
	}
	for _, service := range z.services {
		for _, spec := range servicePorts[service] {
			if portMatches(spec, port, protocol) {
				return "service:" + service
			}
		}
	}
	return ""
}

// firewalldActive reports whether firewalld is running.
func firewalldActive(ctx context.Context, env *Env) bool {
	out, _, err := env.Run(ctx, "systemctl", "is-active", "firewalld")
	return err == nil && strings.TrimSpace(string(out)) == "active"
}

// ListListening lists what listens on the machine and crosses each network
// socket with the active firewalld zones.
func ListListening(ctx context.Context, env *Env) ([]ListeningSocket, error) {
	out, _, err := env.Run(ctx, "ss", "-H", "-tulpn")
	if err != nil {
		return nil, err
	}
	entries := parseSS(string(out))

	virtual := map[string]bool{}
	if addr, _, err := env.Run(ctx, "ip", "-o", "addr", "show"); err == nil {
		for _, line := range strings.Split(string(addr), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 4 && virtualInterface.MatchString(fields[1]) {
				if ip, _, err := net.ParseCIDR(fields[3]); err == nil {
					virtual[ip.String()] = true
				}
			}
		}
	}

	running := firewalldActive(ctx, env)
	var zones []firewallZone
	servicePorts := map[string][]string{}
	if running {
		if zoneOut, _, err := env.Run(ctx, "firewall-cmd", "--list-all-zones"); err == nil {
			for _, zone := range parseFirewallZones(string(zoneOut)) {
				if zone.active || zone.isDefault {
					zones = append(zones, zone)
				}
			}
		}
		// The default zone decides first: it covers interfaces with no zone.
		sort.SliceStable(zones, func(i, j int) bool { return zones[i].isDefault && !zones[j].isDefault })
		for _, zone := range zones {
			for _, service := range zone.services {
				if _, seen := servicePorts[service]; seen {
					continue
				}
				info, _, err := env.Run(ctx, "firewall-cmd", "--info-service="+service)
				if err == nil {
					servicePorts[service] = parseServicePorts(string(info))
				} else {
					servicePorts[service] = nil
				}
			}
		}
	}

	sockets := make([]ListeningSocket, 0, len(entries))
	for _, entry := range entries {
		socket := ListeningSocket{
			Protocol: entry.protocol, Address: entry.address, Port: entry.port,
			Unit: unitOf(env, entry.pid), Reach: reachOf(entry.address, entry.iface, virtual),
		}
		if entry.pid > 0 && env.Readlink != nil {
			socket.Executable, _ = env.Readlink("/proc/" + strconv.Itoa(entry.pid) + "/exe")
		}
		if socket.Reach == "network" {
			socket.State = "filtered"
			if !running {
				socket.State = "exposed"
			}
			for _, zone := range zones {
				if !zone.reachesNetwork() {
					continue
				}
				if allow := zone.allowedBy(entry.port, entry.protocol, servicePorts); allow != "" {
					socket.State, socket.Zone, socket.Allow = "exposed", zone.name, allow
					break
				}
			}
		}
		sockets = append(sockets, socket)
	}
	return sockets, nil
}
