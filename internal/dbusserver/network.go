package dbusserver

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

type NetworkService struct {
	activity *Activity
}

type NetworkInterfaceInfo struct {
	Name     string
	Type     string
	State    string
	IPv4     string
	IPv6     string
	Gateway  string
	DNS      string
	MAC      string
	Speed    string
	SSID     string
	Signal   uint32
	Device   string
	Autoconf bool
}

type WifiNetworkInfo struct {
	SSID     string
	Security string
	Signal   uint32
	Active   bool
	Device   string
}

type ProxyConfig struct {
	HTTP  string
	HTTPS string
	SOCKS string
	No    string
}

func (n *NetworkService) ListInterfaces() ([]NetworkInterfaceInfo, *dbus.Error) {
	n.activity.Touch()
	if !commandAvailable("nmcli") {
		return fallbackNetworkInterfaces(), nil
	}
	out, err := runCommandOutput("nmcli", "-t", "-f", "DEVICE,TYPE,STATE,CONNECTION", "device", "status")
	if err != nil {
		return nil, dbus.MakeFailedError(fmt.Errorf("nmcli device status: %w", err))
	}
	var rows []NetworkInterfaceInfo
	for _, line := range strings.Split(out, "\n") {
		fields := splitNM(line)
		if len(fields) < 4 || fields[0] == "" || fields[0] == "lo" {
			continue
		}
		device := fields[0]
		info := NetworkInterfaceInfo{
			Name:     fields[3],
			Type:     fields[1],
			State:    fields[2],
			Device:   device,
			MAC:      readSysfs(device, "address"),
			Speed:    linkSpeed(device),
			Autoconf: true,
		}
		if info.Name == "" || info.Name == "--" {
			info.Name = device
		}
		fillIPDetails(&info)
		rows = append(rows, info)
	}
	return rows, nil
}

func (n *NetworkService) ListWifi() ([]WifiNetworkInfo, *dbus.Error) {
	n.activity.Touch()
	if !commandAvailable("nmcli") {
		return []WifiNetworkInfo{}, nil
	}
	out, err := runCommandOutput("nmcli", "-t", "-f", "IN-USE,SSID,SECURITY,SIGNAL,DEVICE", "device", "wifi", "list", "--rescan", "yes")
	if err != nil {
		return []WifiNetworkInfo{}, nil
	}
	var rows []WifiNetworkInfo
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := splitNM(line)
		if len(fields) < 5 || fields[1] == "" {
			continue
		}
		key := fields[4] + "\x00" + fields[1]
		if seen[key] {
			continue
		}
		seen[key] = true
		signal, _ := strconv.ParseUint(fields[3], 10, 32)
		rows = append(rows, WifiNetworkInfo{
			SSID:     fields[1],
			Security: fields[2],
			Signal:   uint32(signal),
			Active:   fields[0] == "*",
			Device:   fields[4],
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Signal > rows[j].Signal })
	return rows, nil
}

func (n *NetworkService) ConnectWifi(sender dbus.Sender, ssid string, password string) *dbus.Error {
	n.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.network.configure"); err != nil {
		return err
	}
	if !commandAvailable("nmcli") {
		return dbus.MakeFailedError(fmt.Errorf("NetworkManager/nmcli não está disponível"))
	}
	args := []string{"device", "wifi", "connect", ssid}
	if password != "" {
		args = append(args, "password", password)
	}
	if err := runCommand("nmcli", args...); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (n *NetworkService) Disconnect(sender dbus.Sender, device string) *dbus.Error {
	n.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.network.configure"); err != nil {
		return err
	}
	if !commandAvailable("nmcli") {
		return dbus.MakeFailedError(fmt.Errorf("NetworkManager/nmcli não está disponível"))
	}
	if err := runCommand("nmcli", "device", "disconnect", device); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (n *NetworkService) SetStaticIPv4(sender dbus.Sender, connection string, address string, gateway string, dns string) *dbus.Error {
	n.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.network.configure"); err != nil {
		return err
	}
	if !commandAvailable("nmcli") {
		return dbus.MakeFailedError(fmt.Errorf("NetworkManager/nmcli não está disponível"))
	}
	if err := runCommand("nmcli", "connection", "modify", connection, "ipv4.method", "manual", "ipv4.addresses", address, "ipv4.gateway", gateway, "ipv4.dns", dns); err != nil {
		return dbus.MakeFailedError(err)
	}
	if err := runCommand("nmcli", "connection", "up", connection); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (n *NetworkService) ImportVPN(sender dbus.Sender, path string) *dbus.Error {
	n.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.network.configure"); err != nil {
		return err
	}
	if !commandAvailable("nmcli") {
		return dbus.MakeFailedError(fmt.Errorf("NetworkManager/nmcli não está disponível"))
	}
	if err := runCommand("nmcli", "connection", "import", "type", "openvpn", "file", path); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (n *NetworkService) GetProxy() (ProxyConfig, *dbus.Error) {
	n.activity.Touch()
	return readProxyConfig(), nil
}

func (n *NetworkService) SetProxy(sender dbus.Sender, http string, https string, socks string, no string) *dbus.Error {
	n.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.network.configure"); err != nil {
		return err
	}
	if err := writeProxyConfig(ProxyConfig{HTTP: http, HTTPS: https, SOCKS: socks, No: no}); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func splitNM(line string) []string {
	return strings.Split(strings.ReplaceAll(line, `\:`, "\x00"), ":")
}

func fillIPDetails(info *NetworkInterfaceInfo) {
	if !commandAvailable("nmcli") {
		return
	}
	out, err := runCommandOutput("nmcli", "-t", "-f", "IP4.ADDRESS,IP4.GATEWAY,IP4.DNS,IP6.ADDRESS", "device", "show", info.Device)
	if err != nil {
		return
	}
	var dns []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "IP4.ADDRESS") && info.IPv4 == "" {
			info.IPv4 = valueAfterColon(line)
		} else if strings.HasPrefix(line, "IP4.GATEWAY") && info.Gateway == "" {
			info.Gateway = valueAfterColon(line)
		} else if strings.HasPrefix(line, "IP4.DNS") {
			dns = append(dns, valueAfterColon(line))
		} else if strings.HasPrefix(line, "IP6.ADDRESS") && info.IPv6 == "" {
			info.IPv6 = valueAfterColon(line)
		}
	}
	info.DNS = strings.Join(dns, ", ")
}

func valueAfterColon(line string) string {
	if idx := strings.Index(line, ":"); idx >= 0 {
		return strings.TrimSpace(line[idx+1:])
	}
	return ""
}

func fallbackNetworkInterfaces() []NetworkInterfaceInfo {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return []NetworkInterfaceInfo{}
	}
	var rows []NetworkInterfaceInfo
	for _, entry := range entries {
		name := entry.Name()
		if name == "lo" {
			continue
		}
		rows = append(rows, NetworkInterfaceInfo{
			Name:   name,
			Type:   "interface",
			State:  readSysfs(name, "operstate"),
			MAC:    readSysfs(name, "address"),
			Speed:  linkSpeed(name),
			Device: name,
		})
	}
	return rows
}

func readSysfs(device string, file string) string {
	value, err := readTrimmedFile("/sys/class/net/" + device + "/" + file)
	if err != nil {
		return ""
	}
	return value
}

func linkSpeed(device string) string {
	value := readSysfs(device, "speed")
	if value == "" || value == "-1" {
		return ""
	}
	return value + " Mb/s"
}

func readProxyConfig() ProxyConfig {
	cfg := ProxyConfig{}
	data, err := os.ReadFile("/etc/environment")
	if err != nil {
		return cfg
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := parseEnvLine(line)
		if !ok {
			continue
		}
		switch strings.ToLower(key) {
		case "http_proxy":
			cfg.HTTP = value
		case "https_proxy":
			cfg.HTTPS = value
		case "all_proxy":
			cfg.SOCKS = value
		case "no_proxy":
			cfg.No = value
		}
	}
	return cfg
}

func writeProxyConfig(cfg ProxyConfig) error {
	path := "/etc/environment"
	lines := []string{}
	if data, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			key, _, ok := parseEnvLine(line)
			if ok {
				switch strings.ToLower(key) {
				case "http_proxy", "https_proxy", "all_proxy", "no_proxy":
					continue
				}
			}
			if strings.TrimSpace(line) != "" {
				lines = append(lines, line)
			}
		}
	}
	addProxyLine := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			lines = append(lines, fmt.Sprintf("%s=%q", key, value))
		}
	}
	addProxyLine("http_proxy", cfg.HTTP)
	addProxyLine("https_proxy", cfg.HTTPS)
	addProxyLine("all_proxy", cfg.SOCKS)
	addProxyLine("no_proxy", cfg.No)
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

func parseEnvLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
		return "", "", false
	}
	parts := strings.SplitN(line, "=", 2)
	value := strings.Trim(parts[1], `"'`)
	return strings.TrimSpace(parts[0]), value, true
}
