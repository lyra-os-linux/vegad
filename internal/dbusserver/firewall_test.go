package dbusserver

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFirewallStub(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
}

// From SSH or the vega-web broker, `firewall-cmd --state` fails with
// "Authorization failed" (vegad#79) while the active zone query succeeds.
// Status must report the firewall through systemd instead.
func TestFirewallStatusDoesNotNeedFirewalldAuthorization(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	writeFirewallStub(t, bin, "firewall-cmd", `case "$*" in
--state) echo 'Authorization failed.'; exit 253;;
--get-active-zone) printf 'public\n  interfaces: eth0\n';;
*) exit 1;;
esac
`)
	writeFirewallStub(t, bin, "systemctl", `case "$*" in
'is-active firewalld.service') echo active;;
*) exit 1;;
esac
`)
	service := &FirewallService{activity: &Activity{}}
	running, zone, derr := service.Status()
	if derr != nil {
		t.Fatalf("status failed: %v", derr)
	}
	if !running || zone != "public" {
		t.Fatalf("expected a running firewall in zone public, got running=%v zone=%q", running, zone)
	}
}

func TestFirewallStatusReportsStoppedFirewalld(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	writeFirewallStub(t, bin, "firewall-cmd", "exit 253\n")
	writeFirewallStub(t, bin, "systemctl", "echo inactive; exit 3\n")
	service := &FirewallService{activity: &Activity{}}
	running, zone, derr := service.Status()
	if derr != nil || running || zone != "" {
		t.Fatalf("expected a stopped firewall without error, got running=%v zone=%q err=%v", running, zone, derr)
	}
}

// firewalld ships a predefined "docker" zone bound to docker0. It is listed
// as active even when the interface does not exist, and sorts before
// "public"; Status must report the zone that filters real traffic (vegad#83).
func TestFirstActiveZonePrefersZoneBoundToExistingInterface(t *testing.T) {
	existing := map[string]bool{"enp0s2": true, "eth0": true}
	original := netInterfaceExists
	netInterfaceExists = func(name string) bool { return existing[name] }
	t.Cleanup(func() { netInterfaceExists = original })

	cases := []struct {
		name, output, want string
	}{
		{"docker zone without docker0", "docker\n  interfaces: docker0\npublic (default)\n  interfaces: enp0s2\n", "public"},
		{"single zone", "public\n  interfaces: eth0\n", "public"},
		{"default zone unbound, other bound", "home\n  interfaces: enp0s2\npublic (default)\n  interfaces: wlan9\n", "home"},
		{"source-bound zone", "docker\n  interfaces: docker0\ntrusted\n  sources: 10.0.0.0/8\n", "trusted"},
		{"nothing bound falls back to default", "docker\n  interfaces: docker0\npublic (default)\n  interfaces: wlan9\n", "public"},
		{"nothing bound, no default", "docker\n  interfaces: docker0\n", "docker"},
		{"default and active marker", "block\n  interfaces: docker0\nwork (default, active)\n  interfaces: eth0\n", "work"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstActiveZone(tc.output); got != tc.want {
				t.Fatalf("firstActiveZone(%q) = %q, want %q", tc.output, got, tc.want)
			}
		})
	}
}
