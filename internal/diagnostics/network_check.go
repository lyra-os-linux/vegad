package diagnostics

import (
	"bufio"
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The "network" test (add-network-troubleshooter): where the connection
// breaks, in order — link, address, gateway, DNS, internet — so the first
// failing step is the answer. Read-only; the fixes are the reconnect,
// renew-dhcp and temporary-dns remedies.

// Step statuses in the "steps" metric.
const (
	stepOK      = "ok"
	stepFail    = "fail"
	stepWarn    = "warn"
	stepSkipped = "skipped"
)

type networkStep struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type nmDevice struct {
	Name, Type, State, Connection string
}

// splitTerse splits one line of "nmcli -t", where ":" separates fields and
// "\:" is a literal colon (SSIDs may have one).
func splitTerse(line string) []string {
	var fields []string
	var field strings.Builder
	escaped := false
	for _, r := range line {
		switch {
		case escaped:
			field.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == ':':
			fields = append(fields, field.String())
			field.Reset()
		default:
			field.WriteRune(r)
		}
	}
	return append(fields, field.String())
}

func parseNMDevices(out []byte) []nmDevice {
	var devices []nmDevice
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		f := splitTerse(scanner.Text())
		if len(f) < 4 {
			continue
		}
		devices = append(devices, nmDevice{f[0], f[1], f[2], f[3]})
	}
	return devices
}

// activeWifiSignal reads "nmcli -t -f ACTIVE,SIGNAL device wifi list".
func activeWifiSignal(out []byte) (int, bool) {
	for _, line := range strings.Split(string(out), "\n") {
		f := splitTerse(line)
		if len(f) >= 2 && f[0] == "yes" {
			signal, err := strconv.Atoi(f[1])
			return signal, err == nil
		}
	}
	return 0, false
}

type ipAddrInfo struct {
	Family string `json:"family"`
	Local  string `json:"local"`
	Scope  string `json:"scope"`
}

type ipLink struct {
	IfName   string       `json:"ifname"`
	AddrInfo []ipAddrInfo `json:"addr_info"`
}

// globalAddresses lists the global-scope addresses of dev from "ip -j addr".
func globalAddresses(out []byte, dev string) []string {
	var links []ipLink
	_ = json.Unmarshal(out, &links)
	var addrs []string
	for _, link := range links {
		if link.IfName != dev {
			continue
		}
		for _, addr := range link.AddrInfo {
			if addr.Scope == "global" {
				addrs = append(addrs, addr.Local)
			}
		}
	}
	return addrs
}

type ipRoute struct {
	Gateway string `json:"gateway"`
	Dev     string `json:"dev"`
}

// defaultGateway prefers the default route of dev, then any default route.
func defaultGateway(out []byte, dev string) string {
	var routes []ipRoute
	_ = json.Unmarshal(out, &routes)
	for _, route := range routes {
		if route.Dev == dev && route.Gateway != "" {
			return route.Gateway
		}
	}
	for _, route := range routes {
		if route.Gateway != "" {
			return route.Gateway
		}
	}
	return ""
}

type ipNeighbor struct {
	State []string `json:"state"`
}

// neighborFailed reads "ip -j neigh show <gw>": the gateway did not answer
// ARP/NDP. Unknown or stale entries do not count as a failure.
func neighborFailed(out []byte) bool {
	var neighbors []ipNeighbor
	_ = json.Unmarshal(out, &neighbors)
	for _, neighbor := range neighbors {
		for _, state := range neighbor.State {
			if state == "FAILED" || state == "INCOMPLETE" {
				return true
			}
		}
	}
	return false
}

const defaultCheckHost = "conncheck.opensuse.org"

var hostPattern = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)

// connectivityURI reads [connectivity] uri from "NetworkManager
// --print-config": the address NetworkManager itself checks, which also
// detects captive portals.
func connectivityURI(out []byte) string {
	section := ""
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		if section == "[connectivity]" {
			if value, ok := strings.CutPrefix(line, "uri="); ok {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func runNetwork(ctx context.Context, env *Env, progress Progress) StageResult {
	start := time.Now()
	result := StageResult{ID: "network", Metrics: map[string]any{}}
	have := func(tool string) bool { _, err := env.LookPath(tool); return err == nil }
	steps := []networkStep{}
	var findings []Finding
	skipRest := func(ids ...string) {
		for _, id := range ids {
			steps = append(steps, networkStep{ID: id, Status: stepSkipped})
		}
	}
	finish := func() StageResult {
		result.Metrics["steps"] = steps
		result.Findings = findings
		result.Verdict = verdictFromFindings(findings)
		failed := ""
		for _, step := range steps {
			if step.Status == stepFail {
				failed = step.ID
				break
			}
		}
		if failed == "" {
			result.Summary = env.t("diagnostics.summary.network.ok")
		} else {
			result.Summary = env.t("diagnostics.summary.network.failed", "step", env.t("diagnostics.network.step."+failed))
		}
		result.DurationSeconds = int(time.Since(start).Seconds())
		return result
	}

	// 1. Link: an Ethernet or Wi-Fi device connected.
	progress(10, env.t("diagnostics.progress.network.link"))
	out, _, err := env.Run(ctx, "nmcli", "-t", "-f", "DEVICE,TYPE,STATE,CONNECTION", "device")
	if err != nil {
		return failedToRun(env, result, "nmcli", err, start)
	}
	var active, idle *nmDevice
	devices := parseNMDevices(out)
	for i := range devices {
		device := &devices[i]
		if device.Type != "ethernet" && device.Type != "wifi" {
			continue
		}
		if device.State == "connected" && active == nil {
			active = device
		}
		// A device NetworkManager can connect right now (Wi-Fi in range,
		// cable plugged but not activated).
		if device.State == "disconnected" && idle == nil {
			idle = device
		}
	}
	if active == nil {
		var wifiOff bool
		if out, _, err := env.Run(ctx, "nmcli", "radio", "wifi"); err == nil {
			wifiOff = strings.TrimSpace(string(out)) == "disabled"
		}
		detail := ""
		finding := env.finding(SeverityFail, "network.no-link", strings.TrimSpace(string(out)))
		target := idle
		if target == nil && wifiOff {
			for i := range devices {
				if devices[i].Type == "wifi" {
					target = &devices[i]
					break
				}
			}
		}
		if target != nil && interfacePattern.MatchString(target.Name) {
			finding.Remedy = "reconnect"
			finding.RemedyArgs = []string{target.Name}
			detail = target.Name
		}
		result.Metrics["wifiEnabled"] = !wifiOff
		steps = append(steps, networkStep{ID: "link", Status: stepFail, Detail: detail})
		findings = append(findings, finding)
		skipRest("address", "gateway", "dns", "internet")
		return finish()
	}
	linkDetail := active.Name
	if active.Type == "wifi" {
		result.Sensitive = append(result.Sensitive, active.Connection)
		linkDetail = active.Name + " · Wi-Fi " + active.Connection
		if out, _, err := env.Run(ctx, "nmcli", "-t", "-f", "ACTIVE,SIGNAL", "device", "wifi", "list", "--rescan", "no"); err == nil {
			if signal, ok := activeWifiSignal(out); ok {
				result.Metrics["wifiSignal"] = signal
				linkDetail += " · " + strconv.Itoa(signal) + "%"
				if signal < 30 {
					findings = append(findings, env.finding(SeverityAttention, "network.wifi-weak", strconv.Itoa(signal)+"%", "signal", strconv.Itoa(signal)))
				}
			}
		}
	}
	result.Metrics["device"] = active.Name
	linkStatus := stepOK
	if len(findings) > 0 {
		linkStatus = stepWarn
	}
	steps = append(steps, networkStep{ID: "link", Status: linkStatus, Detail: linkDetail})

	// 2. Address: a global address on the connected device.
	progress(25, env.t("diagnostics.progress.network.address"))
	out, _, err = env.Run(ctx, "ip", "-j", "addr", "show", "dev", active.Name)
	if err != nil {
		return failedToRun(env, result, "ip", err, start)
	}
	addrs := globalAddresses(out, active.Name)
	if len(addrs) == 0 {
		finding := env.finding(SeverityFail, "network.no-address", active.Name)
		if interfacePattern.MatchString(active.Name) {
			finding.Remedy = "renew-dhcp"
			finding.RemedyArgs = []string{active.Name}
		}
		findings = append(findings, finding)
		steps = append(steps, networkStep{ID: "address", Status: stepFail})
		skipRest("gateway", "dns", "internet")
		return finish()
	}
	result.Sensitive = append(result.Sensitive, addrs...)
	steps = append(steps, networkStep{ID: "address", Status: stepOK, Detail: strings.Join(addrs, ", ")})

	// 3. Gateway: answered last time we asked. Many routers ignore ping, so a
	// silent gateway is the cause only if DNS or the internet also fail.
	progress(45, env.t("diagnostics.progress.network.gateway"))
	out, _, err = env.Run(ctx, "ip", "-j", "route", "show", "default")
	if err != nil {
		return failedToRun(env, result, "ip", err, start)
	}
	gateway := defaultGateway(out, active.Name)
	gatewayOK := gateway != ""
	if gateway != "" {
		if have("ping") {
			pingCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			_, code, err := env.Run(pingCtx, "ping", "-c", "2", "-W", "2", gateway)
			cancel()
			gatewayOK = err == nil && code == 0
		} else if out, _, err := env.Run(ctx, "ip", "-j", "neigh", "show", gateway); err == nil {
			gatewayOK = !neighborFailed(out)
		}
	}
	gatewayStep := networkStep{ID: "gateway", Status: stepOK, Detail: gateway}

	// 4. DNS: resolve the host NetworkManager checks.
	progress(65, env.t("diagnostics.progress.network.dns"))
	uri := ""
	if have("NetworkManager") {
		if out, _, err := env.Run(ctx, "NetworkManager", "--print-config"); err == nil {
			uri = connectivityURI(out)
		}
	}
	host := defaultCheckHost
	if parsed, err := url.Parse(uri); err == nil && hostPattern.MatchString(parsed.Hostname()) {
		host = parsed.Hostname()
	}
	dnsCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	_, dnsCode, dnsErr := env.Run(dnsCtx, "getent", "ahosts", host)
	cancel()
	if ctx.Err() != nil {
		return failedToRun(env, result, "getent", ctx.Err(), start)
	}
	dnsOK := dnsErr == nil && dnsCode == 0

	// 5. Internet: NetworkManager's own check (full, portal, limited, none).
	progress(85, env.t("diagnostics.progress.network.internet"))
	connectivity := "unknown"
	if out, _, err := env.Run(ctx, "nmcli", "networking", "connectivity", "check"); err == nil {
		connectivity = strings.TrimSpace(string(out))
	}
	result.Metrics["connectivity"] = connectivity
	if uri != "" {
		result.Metrics["portalUrl"] = uri
	}

	switch {
	case !gatewayOK && (!dnsOK || connectivity == "none" || connectivity == "limited"):
		gatewayStep.Status = stepFail
		raw := gateway
		if gateway == "" {
			raw = "sem rota padrão"
		}
		steps = append(steps, gatewayStep)
		findings = append(findings, env.finding(SeverityFail, "network.gateway-unreachable", raw))
		skipRest("dns", "internet")
		return finish()
	case !gatewayOK:
		gatewayStep.Status = stepWarn
		findings = append(findings, env.finding(Info, "network.gateway-no-ping", gateway))
	}
	steps = append(steps, gatewayStep)

	if !dnsOK {
		finding := env.finding(SeverityFail, "network.dns-failed", host)
		if interfacePattern.MatchString(active.Name) {
			finding.Remedy = "temporary-dns"
			finding.RemedyArgs = []string{active.Name}
		}
		findings = append(findings, finding)
		steps = append(steps, networkStep{ID: "dns", Status: stepFail, Detail: host})
		skipRest("internet")
		return finish()
	}
	steps = append(steps, networkStep{ID: "dns", Status: stepOK, Detail: host})

	switch connectivity {
	case "full":
		steps = append(steps, networkStep{ID: "internet", Status: stepOK})
	case "portal":
		findings = append(findings, env.finding(SeverityAttention, "network.captive-portal", uri))
		steps = append(steps, networkStep{ID: "internet", Status: stepWarn, Detail: "portal"})
	case "limited", "none":
		findings = append(findings, env.finding(SeverityFail, "network.no-internet", connectivity))
		steps = append(steps, networkStep{ID: "internet", Status: stepFail, Detail: connectivity})
	default:
		// Connectivity checking off: DNS works, which is as far as we know.
		findings = append(findings, env.finding(Info, "network.connectivity-unknown", connectivity))
		steps = append(steps, networkStep{ID: "internet", Status: stepSkipped, Detail: connectivity})
	}

	// VPNs change where traffic goes; worth knowing, not a fault.
	if out, _, err := env.Run(ctx, "nmcli", "-t", "-f", "NAME,TYPE", "connection", "show", "--active"); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			f := splitTerse(line)
			if len(f) >= 2 && (f[1] == "vpn" || f[1] == "wireguard") {
				findings = append(findings, env.finding(Info, "network.vpn", f[0], "name", f[0]))
				break
			}
		}
	}
	return finish()
}
