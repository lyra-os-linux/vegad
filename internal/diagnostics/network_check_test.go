package diagnostics

import (
	"context"
	"strings"
	"testing"
)

// workstation is the real state captured on 30/10/2026 (SSID and addresses
// replaced): Wi-Fi connected, cable unplugged, no ping installed.
func workstation(t *testing.T) map[string]call {
	return map[string]call{
		"nmcli -t -f DEVICE,TYPE,STATE,CONNECTION device":        {out: fixture(t, "nmcli-device.txt")},
		"nmcli -t -f ACTIVE,SIGNAL device wifi list --rescan no": {out: fixture(t, "nmcli-wifi-list.txt")},
		"ip -j addr show dev wlp0s20f3":                          {out: fixture(t, "ip-addr.json")},
		"ip -j route show default":                               {out: fixture(t, "ip-route-default.json")},
		"ip -j neigh show 192.168.15.1":                          {out: []byte(`[{"dst":"192.168.15.1","dev":"wlp0s20f3","state":["REACHABLE"]}]`)},
		"NetworkManager --print-config":                          {out: []byte("[main]\nplugins=keyfile\n\n[connectivity]\nuri=http://conncheck.opensuse.org\n\n[logging]\n")},
		"getent ahosts conncheck.opensuse.org":                   {out: []byte("2a07:de40:b27e:1204::10 STREAM conncheck.opensuse.org\n")},
		"nmcli networking connectivity check":                    {out: []byte("full\n")},
		"nmcli -t -f NAME,TYPE connection show --active":         {out: []byte("CasaWiFi:802-11-wireless\nlo:loopback\n")},
	}
}

var networkTools = []string{"nmcli", "ip", "getent", "NetworkManager"}

func steps(result StageResult) string {
	var parts []string
	for _, step := range result.Metrics["steps"].([]networkStep) {
		parts = append(parts, step.ID+"="+step.Status)
	}
	return strings.Join(parts, " ")
}

func TestNetworkOnTheWorkstationIsHealthy(t *testing.T) {
	result := runNetwork(context.Background(), fakeEnv(t, workstation(t), nil, networkTools...), noProgress)
	if result.Verdict != Ok || len(result.Findings) != 0 {
		t.Fatalf("verdict %s findings %+v", result.Verdict, result.Findings)
	}
	if got := steps(result); got != "link=ok address=ok gateway=ok dns=ok internet=ok" {
		t.Fatal(got)
	}
	if result.Metrics["wifiSignal"] != 64 || result.Metrics["device"] != "wlp0s20f3" {
		t.Fatalf("metrics %+v", result.Metrics)
	}
	// The SSID and the addresses leave a redacted report.
	if !strings.Contains(strings.Join(result.Sensitive, " "), "CasaWiFi") || !strings.Contains(strings.Join(result.Sensitive, " "), "192.168.15.186") {
		t.Fatalf("sensitive %v", result.Sensitive)
	}
}

func TestNetworkWithoutLinkOffersReconnectAndSkipsTheRest(t *testing.T) {
	calls := workstation(t)
	calls["nmcli -t -f DEVICE,TYPE,STATE,CONNECTION device"] = call{out: []byte("wlp0s20f3:wifi:disconnected:\nenp44s0:ethernet:unavailable:\nlo:loopback:connected (externally):lo\n")}
	calls["nmcli radio wifi"] = call{out: []byte("enabled\n")}
	result := runNetwork(context.Background(), fakeEnv(t, calls, nil, networkTools...), noProgress)
	link := findingCodes(result.Findings)["network.no-link"]
	if result.Verdict != Fail || link.Remedy != "reconnect" || strings.Join(link.RemedyArgs, ",") != "wlp0s20f3" {
		t.Fatalf("%s %+v", result.Verdict, result.Findings)
	}
	if got := steps(result); got != "link=fail address=skipped gateway=skipped dns=skipped internet=skipped" {
		t.Fatal(got)
	}
}

func TestNetworkWithWifiOffTurnsItOn(t *testing.T) {
	calls := workstation(t)
	calls["nmcli -t -f DEVICE,TYPE,STATE,CONNECTION device"] = call{out: []byte("wlp0s20f3:wifi:unavailable:\nenp44s0:ethernet:unavailable:\n")}
	calls["nmcli radio wifi"] = call{out: []byte("disabled\n")}
	result := runNetwork(context.Background(), fakeEnv(t, calls, nil, networkTools...), noProgress)
	if link := findingCodes(result.Findings)["network.no-link"]; link.Remedy != "reconnect" || result.Metrics["wifiEnabled"] != false {
		t.Fatalf("%+v %+v", link, result.Metrics)
	}
}

func TestNetworkWithCableUnpluggedHasNoAutomaticFix(t *testing.T) {
	calls := workstation(t)
	calls["nmcli -t -f DEVICE,TYPE,STATE,CONNECTION device"] = call{out: []byte("enp44s0:ethernet:unavailable:\n")}
	calls["nmcli radio wifi"] = call{out: []byte("enabled\n")}
	result := runNetwork(context.Background(), fakeEnv(t, calls, nil, networkTools...), noProgress)
	if link := findingCodes(result.Findings)["network.no-link"]; link.Severity != SeverityFail || link.Remedy != "" {
		t.Fatalf("%+v", link)
	}
}

func TestNetworkWithoutAddressOffersDHCP(t *testing.T) {
	calls := workstation(t)
	calls["ip -j addr show dev wlp0s20f3"] = call{out: []byte(`[{"ifname":"wlp0s20f3","addr_info":[{"family":"inet6","local":"fe80::1","scope":"link"}]}]`)}
	result := runNetwork(context.Background(), fakeEnv(t, calls, nil, networkTools...), noProgress)
	if f := findingCodes(result.Findings)["network.no-address"]; f.Remedy != "renew-dhcp" {
		t.Fatalf("%+v", result.Findings)
	}
	if got := steps(result); got != "link=ok address=fail gateway=skipped dns=skipped internet=skipped" {
		t.Fatal(got)
	}
}

func TestNetworkWithBrokenDNSOffersTemporaryDNS(t *testing.T) {
	calls := workstation(t)
	calls["getent ahosts conncheck.opensuse.org"] = call{code: 2}
	result := runNetwork(context.Background(), fakeEnv(t, calls, nil, networkTools...), noProgress)
	f := findingCodes(result.Findings)["network.dns-failed"]
	if f.Remedy != "temporary-dns" || strings.Join(f.RemedyArgs, ",") != "wlp0s20f3" {
		t.Fatalf("%+v", result.Findings)
	}
	if got := steps(result); got != "link=ok address=ok gateway=ok dns=fail internet=skipped" {
		t.Fatal(got)
	}
}

func TestNetworkGatewayThatIgnoresPingIsNotTheCause(t *testing.T) {
	calls := workstation(t)
	calls["ping -c 2 -W 2 192.168.15.1"] = call{code: 1}
	result := runNetwork(context.Background(), fakeEnv(t, calls, nil, append(networkTools, "ping")...), noProgress)
	if result.Verdict != Ok || findingCodes(result.Findings)["network.gateway-no-ping"].Severity != Info {
		t.Fatalf("%s %+v", result.Verdict, result.Findings)
	}
}

func TestNetworkSilentGatewayWithNoInternetIsTheCause(t *testing.T) {
	calls := workstation(t)
	calls["ip -j neigh show 192.168.15.1"] = call{out: []byte(`[{"dst":"192.168.15.1","state":["FAILED"]}]`)}
	calls["getent ahosts conncheck.opensuse.org"] = call{code: 2}
	calls["nmcli networking connectivity check"] = call{out: []byte("none\n")}
	result := runNetwork(context.Background(), fakeEnv(t, calls, nil, networkTools...), noProgress)
	if _, ok := findingCodes(result.Findings)["network.gateway-unreachable"]; !ok {
		t.Fatalf("%+v", result.Findings)
	}
	if got := steps(result); got != "link=ok address=ok gateway=fail dns=skipped internet=skipped" {
		t.Fatal(got)
	}
}

func TestNetworkCaptivePortalAndNoInternet(t *testing.T) {
	calls := workstation(t)
	calls["nmcli networking connectivity check"] = call{out: []byte("portal\n")}
	result := runNetwork(context.Background(), fakeEnv(t, calls, nil, networkTools...), noProgress)
	if _, ok := findingCodes(result.Findings)["network.captive-portal"]; !ok || result.Metrics["portalUrl"] != "http://conncheck.opensuse.org" {
		t.Fatalf("%+v %+v", result.Findings, result.Metrics)
	}
	calls["nmcli networking connectivity check"] = call{out: []byte("limited\n")}
	result = runNetwork(context.Background(), fakeEnv(t, calls, nil, networkTools...), noProgress)
	if f := findingCodes(result.Findings)["network.no-internet"]; f.Severity != SeverityFail || f.Remedy != "" {
		t.Fatalf("%+v", result.Findings)
	}
}

func TestNetworkReportsVPNAndWeakSignal(t *testing.T) {
	calls := workstation(t)
	calls["nmcli -t -f ACTIVE,SIGNAL device wifi list --rescan no"] = call{out: []byte("yes:22\n")}
	calls["nmcli -t -f NAME,TYPE connection show --active"] = call{out: []byte("CasaWiFi:802-11-wireless\nTrabalho:wireguard\n")}
	result := runNetwork(context.Background(), fakeEnv(t, calls, nil, networkTools...), noProgress)
	codes := findingCodes(result.Findings)
	if codes["network.wifi-weak"].Severity != SeverityAttention || codes["network.vpn"].Severity != Info || result.Verdict != Attention {
		t.Fatalf("%s %+v", result.Verdict, result.Findings)
	}
}

func TestTerseFieldsKeepEscapedColons(t *testing.T) {
	if got := splitTerse(`wlp0s20f3:wifi:connected:Café\: 2G`); len(got) != 4 || got[3] != "Café: 2G" {
		t.Fatalf("%q", got)
	}
}

func TestNetworkRemediesActOnTheDeviceOnly(t *testing.T) {
	calls := map[string]call{
		"nmcli device modify wlp0s20f3 ipv4.ignore-auto-dns yes ipv4.dns 1.1.1.1 9.9.9.9 ipv6.ignore-auto-dns yes": {},
		"nmcli radio wifi":                         {out: []byte("disabled\n")},
		"nmcli radio wifi on":                      {},
		"nmcli --wait 30 device connect wlp0s20f3": {},
		"nmcli --wait 30 device reapply wlp0s20f3": {code: 10},
	}
	env := fakeEnv(t, calls, nil, "nmcli")
	for _, id := range []string{"temporary-dns", "renew-dhcp"} {
		remedy, _ := LookupRemedy(id)
		if result := remedy.Run(context.Background(), env, []string{"wlp0s20f3"}, noProgress); result.Verdict != Ok {
			t.Fatalf("%s: %+v", id, result)
		}
		if !remedy.NoSnapshot {
			t.Fatalf("%s must skip the snapshot", id)
		}
	}
	// A tampered argument never reaches nmcli.
	remedy, _ := LookupRemedy("reconnect")
	if result := remedy.Run(context.Background(), env, []string{"--help"}, noProgress); result.Verdict != Unavailable {
		t.Fatalf("%+v", result)
	}
}
