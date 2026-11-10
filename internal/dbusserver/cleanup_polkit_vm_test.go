package dbusserver

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// The private-bus integration script has no Polkit authority, so it only
// proves that Scan is denied there (lyra-vega-dbus#21). This VM test covers
// the authorized path with real polkitd and the packaged policy.
func TestCleanupScanPolkitVM(t *testing.T) {
	requireQueryVM(t)
	if out, err := exec.Command("systemctl", "start", "vegad.service").CombinedOutput(); err != nil {
		t.Fatalf("packaged daemon: %v %s", err, out)
	}
	t.Cleanup(func() { exec.Command("systemctl", "stop", "vegad.service").Run() })
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var ready bool
		if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.freedesktop.PolicyKit1").Store(&ready); err != nil {
			t.Fatal(err)
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			log, _ := os.ReadFile("/tmp/polkit-vm.log")
			t.Fatalf("polkitd did not start: %s", log)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var categories []CleanupCategory
	if err := conn.Object(BusName, ObjectPath).Call(BusName+".Cleanup.Scan", 0).Store(&categories); err != nil {
		t.Fatalf("Scan autorizado pelo polkit falhou: %v", err)
	}
	ids := map[string]bool{}
	for _, category := range categories {
		ids[category.Id] = true
	}
	if !ids["coredumps"] || !ids["package-cache"] {
		t.Fatalf("categorias do contrato ausentes: %+v", categories)
	}
}
