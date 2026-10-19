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
