package diagnostics

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// Fixtures: ss, firewall-cmd and lsblk from the reference workstation on
// 2026-10-29 (CUPS and aria2c on loopback, DHCPv6 on link-local; zone
// public allows ssh and dhcpv6-client, zone docker accepts all on docker0).

func securityCalls(t *testing.T, ss string, firewallActive bool, extra map[string]call) map[string]call {
	t.Helper()
	state := "inactive\n"
	if firewallActive {
		state = "active\n"
	}
	calls := map[string]call{
		"ss -H -tulpn":                                                     {out: []byte(ss)},
		"ip -o addr show":                                                  {out: []byte("3: wlp0s20f3    inet 192.168.15.5/24 brd 192.168.15.255 scope global\n4: docker0    inet 172.17.0.1/16 brd 172.17.255.255 scope global docker0\n")},
		"systemctl is-active firewalld":                                    {out: []byte(state)},
		"firewall-cmd --list-all-zones":                                    {out: fixture(t, "firewall-zones.txt")},
		"firewall-cmd --info-service=ssh":                                  {out: fixture(t, "firewall-service-ssh.txt")},
		"firewall-cmd --info-service=dhcpv6-client":                        {out: fixture(t, "firewall-service-dhcpv6-client.txt")},
		"zypper -n --no-refresh --xmlout list-patches --category security": {out: fixture(t, "patches-security-none.xml")},
		"systemctl is-enabled sshd":                                        {out: []byte("disabled\n"), code: 1},
		"systemctl is-active sshd":                                         {out: []byte("inactive\n"), code: 3},
		"getenforce":                                                       {out: []byte("Disabled\n")},
		"mokutil --sb-state":                                               {out: []byte("SecureBoot enabled\n")},
		"findmnt -n -o SOURCE /":                                           {out: []byte("/dev/nvme0n1p2[/@]\n")},
		"lsblk -J -s -o NAME,TYPE /dev/nvme0n1p2":                          {out: fixture(t, "lsblk-root-plain.json")},
	}
	for key, value := range extra {
		calls[key] = value
	}
	return calls
}

func securityFiles() map[string]string {
	return map[string]string{
		"/etc/passwd":        "root:x:0:0:root:/root:/bin/bash\nalice:x:1001:1001::/home/alice:/bin/bash\n",
		"/etc/shadow":        "root:!:20000::::::\nalice:$6$salt$hash:20000:0:99999:7:::\n",
		"/etc/group":         "wheel:x:10:alice\n",
		"/proc/1213/cgroup":  "0::/system.slice/cups.service\n",
		"/proc/82956/cgroup": "0::/user.slice/user-1001.slice/user@1001.service/app.slice/app-gnome-org.lyraos.Downloads-1.scope\n",
		"/proc/1162/cgroup":  "0::/system.slice/NetworkManager.service\n",
		"/proc/4242/cgroup":  "0::/user.slice/user-1001.slice/user@1001.service/app.slice/vte-spawn-1.scope\n",
		"/proc/22/cgroup":    "0::/system.slice/sshd.service\n",
	}
}

func securityEnv(t *testing.T, calls map[string]call, files map[string]string) *Env {
	env := fakeEnv(t, calls, files, "zypper", "mokutil", "ss")
	env.Readlink = func(path string) (string, error) {
		exe := map[string]string{"/proc/1213/exe": "/usr/sbin/cupsd", "/proc/82956/exe": "/usr/bin/aria2c", "/proc/1162/exe": "/usr/sbin/NetworkManager", "/proc/4242/exe": "/usr/bin/python3.13", "/proc/22/exe": "/usr/sbin/sshd"}[path]
		if exe == "" {
			return "", os.ErrNotExist
		}
		return exe, nil
	}
	return env
}

func TestListListeningOnTheWorkstationExposesNothing(t *testing.T) {
	env := securityEnv(t, securityCalls(t, string(fixture(t, "ss-tulpn.txt")), true, nil), securityFiles())
	sockets, err := ListListening(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if len(sockets) != 5 {
		t.Fatalf("sockets %+v", sockets)
	}
	for _, s := range sockets {
		if s.State == "exposed" {
			t.Fatalf("nothing on the workstation is exposed: %+v", s)
		}
		switch s.Port {
		case 631:
			if s.Reach != "local" || s.Unit != "cups.service" || s.Executable != "/usr/sbin/cupsd" {
				t.Errorf("cups %+v", s)
			}
		case 42759:
			if s.Reach != "local" || s.Unit != "" {
				t.Errorf("aria2c runs in an app scope, not a service: %+v", s)
			}
		case 546:
			if s.Reach != "link" || s.Protocol != "udp" {
				t.Errorf("DHCPv6 %+v", s)
			}
		}
	}
}

const ssExposed = `tcp LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=22,fd=3))
tcp LISTEN 0 5 0.0.0.0:8000 0.0.0.0:* users:(("python3",pid=4242,fd=3))
tcp LISTEN 0 5 172.17.0.1:9000 0.0.0.0:* users:(("python3",pid=4242,fd=4))
`

func TestFirewallDecidesWhatIsExposed(t *testing.T) {
	env := securityEnv(t, securityCalls(t, ssExposed, true, nil), securityFiles())
	sockets, _ := ListListening(context.Background(), env)
	byPort := map[uint16]ListeningSocket{}
	for _, s := range sockets {
		byPort[s.Port] = s
	}
	if s := byPort[22]; s.State != "exposed" || s.Zone != "public" || s.Allow != "service:ssh" || s.Unit != "sshd.service" {
		t.Errorf("ssh allowed by the public zone: %+v", s)
	}
	if s := byPort[8000]; s.State != "filtered" || s.Unit != "" {
		t.Errorf("8000 is open but no zone lets it in: %+v", s)
	}
	if s := byPort[9000]; s.Reach != "link" || s.State != "" {
		t.Errorf("an address on docker0 stays inside the machine: %+v", s)
	}
	// Firewall off: everything on the network is exposed.
	env = securityEnv(t, securityCalls(t, ssExposed, false, nil), securityFiles())
	sockets, _ = ListListening(context.Background(), env)
	for _, s := range sockets {
		if s.Reach == "network" && s.State != "exposed" {
			t.Errorf("firewall off: %+v", s)
		}
	}
}

func TestPortRangesAndZoneTargets(t *testing.T) {
	if !portMatches("9000-9010/tcp", 9005, "tcp") || portMatches("9000-9010/tcp", 9005, "udp") || portMatches("8000/tcp", 8001, "tcp") {
		t.Fatal("port spec matching")
	}
	docker := firewallZone{name: "docker", target: "ACCEPT", interfaces: []string{"docker0"}}
	if docker.reachesNetwork() {
		t.Fatal("a zone bound only to docker0 does not reach the network")
	}
	if allow := docker.allowedBy(1, "tcp", nil); allow != "target" {
		t.Fatalf("ACCEPT target: %q", allow)
	}
}

func TestSecurityOnTheWorkstation(t *testing.T) {
	env := securityEnv(t, securityCalls(t, string(fixture(t, "ss-tulpn.txt")), true, nil), securityFiles())
	result := runSecurity(context.Background(), env, noProgress)
	codes := findingCodes(result.Findings)
	if result.Verdict != Ok {
		t.Fatalf("verdict %s %+v", result.Verdict, result.Findings)
	}
	for _, code := range []string{"security.selinux", "security.disk-unencrypted"} {
		if codes[code].Severity != Info {
			t.Errorf("%s must be informative: %+v", code, codes[code])
		}
	}
	for _, code := range []string{"security.exposed-port", "security.firewall-off", "security.patches-pending", "security.ssh-root-password"} {
		if _, ok := codes[code]; ok {
			t.Errorf("unexpected %s", code)
		}
	}
	if result.Metrics["exposed"] != 0 || result.Metrics["sshd"] != false || result.Metrics["selinux"] != "disabled" {
		t.Fatalf("metrics %+v", result.Metrics)
	}
}

const patchesTwo = `<?xml version='1.0'?>
<stream>
<update-status version="0.6">
<update-list>
<update kind="patch" name="openSUSE-Leap-16.1-2026-101" category="security" severity="important"/>
<update kind="patch" name="openSUSE-Leap-16.1-2026-102" category="security" severity="moderate"/>
</update-list>
</update-status>
</stream>
`

func TestSecurityFindingsCarryTheirRemedies(t *testing.T) {
	extra := map[string]call{
		"zypper -n --no-refresh --xmlout list-patches --category security": {out: []byte(patchesTwo)},
		"systemctl is-enabled sshd":                                        {out: []byte("enabled\n")},
		"systemctl is-active sshd":                                         {out: []byte("active\n")},
		"sshd -T":                                                          {out: []byte("permitrootlogin yes\npasswordauthentication yes\n")},
		"sudo -n -l -U bob":                                                {out: []byte("User bob may run the following commands on host:\n    (ALL) ALL\n")},
	}
	files := securityFiles()
	files["/etc/passwd"] += "bob:x:1002:1002::/home/bob:/bin/bash\ncarol:x:1003:1003::/home/carol:/bin/bash\n"
	files["/etc/shadow"] += "bob:$6$x$y:20000::::::\ncarol::20000::::::\n"
	extra["sudo -n -l -U carol"] = call{out: []byte("User carol is not allowed to run sudo on host.\n"), code: 1}
	env := securityEnv(t, securityCalls(t, ssExposed, true, extra), files)
	result := runSecurity(context.Background(), env, noProgress)
	codes := findingCodes(result.Findings)
	if result.Verdict != Fail {
		t.Fatalf("verdict %s", result.Verdict)
	}
	if f := codes["security.exposed-port"]; f.Remedy != "close-port" || strings.Join(f.RemedyArgs, ",") != "public|service:ssh" {
		t.Errorf("exposed ssh: %+v", f)
	}
	if f := codes["security.patches-pending"]; f.Remedy != "apply-security-patches" || !strings.Contains(f.Raw, "2026-101 (important)") {
		t.Errorf("patches: %+v", f)
	}
	if codes["security.ssh-root-password"].Severity != SeverityAttention || codes["security.ssh-password"].Severity != SeverityAttention {
		t.Error("desktop SSH with passwords must raise attention")
	}
	if f := codes["security.empty-password"]; f.Severity != SeverityFail || f.Raw != "carol" || strings.Contains(f.Raw, "$6$") {
		t.Errorf("empty password: %+v", f)
	}
	if f := codes["security.sudo-open"]; f.Severity != SeverityFail || f.Raw != "bob" {
		t.Errorf("sudo: %+v", f)
	}
	if strings.Join(result.Sensitive, ",") != "carol,bob" {
		t.Errorf("account names must be redactable: %v", result.Sensitive)
	}
}

func TestServerProfileExpectsSSH(t *testing.T) {
	extra := map[string]call{
		"systemctl is-enabled sshd": {out: []byte("enabled\n")},
		"systemctl is-active sshd":  {out: []byte("active\n")},
		"sshd -T":                   {out: []byte("permitrootlogin prohibit-password\npasswordauthentication yes\n")},
	}
	env := securityEnv(t, securityCalls(t, ssExposed, true, extra), securityFiles())
	env.Profile = "server"
	codes := findingCodes(runSecurity(context.Background(), env, noProgress).Findings)
	if f, ok := codes["security.exposed-port"]; ok {
		t.Errorf("SSH on a server is expected: %+v", f)
	}
	if codes["security.exposed-expected"].Severity != Info || codes["security.ssh-password"].Severity != Info {
		t.Errorf("server findings must be informative: %+v", codes)
	}
	if _, ok := codes["security.ssh-root-password"]; ok {
		t.Error("prohibit-password is not a root password login")
	}
}

func TestFirewallOffProposesEnablingIt(t *testing.T) {
	env := securityEnv(t, securityCalls(t, ssExposed, false, nil), securityFiles())
	codes := findingCodes(runSecurity(context.Background(), env, noProgress).Findings)
	if f := codes["security.firewall-off"]; f.Remedy != "enable-firewall" {
		t.Fatalf("firewall off: %+v", f)
	}
	if f := codes["security.exposed-port"]; f.Remedy != "" {
		t.Errorf("with the firewall off there is no zone to close a port in: %+v", f)
	}
}

func TestLuksRootIsEncrypted(t *testing.T) {
	tree := `{"blockdevices":[{"name":"cr_root","type":"crypt","children":[{"name":"nvme0n1p2","type":"part","children":[{"name":"nvme0n1","type":"disk"}]}]}]}`
	extra := map[string]call{
		"findmnt -n -o SOURCE /":                       {out: []byte("/dev/mapper/cr_root\n")},
		"lsblk -J -s -o NAME,TYPE /dev/mapper/cr_root": {out: []byte(tree)},
	}
	env := securityEnv(t, securityCalls(t, "", true, extra), securityFiles())
	result := runSecurity(context.Background(), env, noProgress)
	if result.Metrics["rootEncrypted"] != true || findingCodes(result.Findings)["security.disk-unencrypted"].Code != "" {
		t.Fatalf("LUKS root: %+v", result.Metrics)
	}
}

func TestClosePortValidatesEveryArgument(t *testing.T) {
	env := fakeEnv(t, map[string]call{
		"firewall-cmd --zone=public --remove-service=ssh --permanent": {},
		"firewall-cmd --zone=public --remove-service=ssh":             {},
		"firewall-cmd --zone=home --remove-port=8000/tcp --permanent": {},
		"firewall-cmd --zone=home --remove-port=8000/tcp":             {},
	}, nil)
	closeRemedy, _ := LookupRemedy("close-port")
	args := []string{"public|service:ssh", "home|port:8000/tcp", "--panic-on|service:ssh", "public|service:--panic-on", "public|port:22", "public|rule:x"}
	result := closeRemedy.Run(context.Background(), env, args, noProgress)
	if result.Verdict != Ok || len(result.Metrics["closed"].([]string)) != 2 {
		t.Fatalf("%+v", result)
	}
	if result := closeRemedy.Run(context.Background(), env, []string{"--panic-on|service:ssh"}, noProgress); result.Verdict != Unavailable {
		t.Fatalf("a tampered argument must never reach firewall-cmd: %+v", result)
	}
}

func TestSecurityRemediesRetestSecurity(t *testing.T) {
	for _, id := range []string{"enable-firewall", "close-port", "apply-security-patches"} {
		remedy, ok := LookupRemedy(id)
		if !ok || remedy.Retest != "security" {
			t.Errorf("%s: %+v", id, remedy)
		}
	}
	env := fakeEnv(t, map[string]call{"zypper -n patch --category security --auto-agree-with-licenses": {out: []byte("done"), code: 102}}, nil)
	patches, _ := LookupRemedy("apply-security-patches")
	result := patches.Run(context.Background(), env, nil, noProgress)
	if result.Verdict != Ok || findingCodes(result.Findings)["repair.restart-needed"].Code == "" {
		t.Fatalf("exit 102 means applied with a restart pending: %+v", result)
	}
	env = fakeEnv(t, map[string]call{"systemctl enable --now firewalld": {err: errors.New("no systemctl")}}, nil)
	firewall, _ := LookupRemedy("enable-firewall")
	if result := firewall.Run(context.Background(), env, nil, noProgress); result.Verdict == Ok {
		t.Fatal("a failed systemctl is not a fix")
	}
}
