package dbusserver

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestQueryAdministrativeVM(t *testing.T) {
	requireQueryVM(t)
	if out, err := exec.Command("zypper", "--non-interactive", "refresh").CombinedOutput(); err != nil {
		t.Fatalf("fixture refresh: %v %s", err, out)
	}
	if out, err := exec.Command("systemctl", "start", "vegad.service").CombinedOutput(); err != nil {
		t.Fatalf("packaged daemon: %v %s", err, out)
	}
	t.Cleanup(func() { exec.Command("systemctl", "stop", "vegad.service").Run() })
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	object := conn.Object(BusName, ObjectPath)
	signals := make(chan *dbus.Signal, 128)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)
	if err := conn.AddMatchSignal(dbus.WithMatchInterface(BusName + ".Software")); err != nil {
		t.Fatal(err)
	}
	transaction := func(method string, args ...interface{}) {
		t.Helper()
		var id uint32
		if err := object.Call(BusName+".Software."+method, 0, args...).Store(&id); err != nil || id == 0 {
			t.Fatalf("%s: transaction not started: %d %v", method, id, err)
		}
		deadline := time.NewTimer(90 * time.Second)
		defer deadline.Stop()
		var console []string
		for {
			select {
			case signal := <-signals:
				if signal.Name != BusName+".Software.TransactionFinished" {
					console = append(console, fmt.Sprint(signal.Body))
					continue
				}
				if len(signal.Body) >= 3 && signal.Body[0] == id {
					if success, ok := signal.Body[1].(bool); !ok || !success {
						t.Fatalf("%s failed: %+v; console: %s", method, signal.Body, strings.Join(console, "\n"))
					}
					t.Logf("%s authorized transaction %d finished", method, id)
					return
				}
			case <-deadline.C:
				t.Fatalf("%s transaction timed out", method)
			}
		}
	}
	transaction("Install", "official", "lyra-query-vm-fixture=1")
	verifyVersion := func(want string) {
		t.Helper()
		version, err := os.ReadFile("/usr/share/lyra-query-vm-fixture/version")
		if err != nil || strings.TrimSpace(string(version)) != want {
			t.Fatalf("fixture version: %q %v", version, err)
		}
		uid, err := os.ReadFile("/var/lib/lyra-query-vm-fixture/scriptlet-uid")
		if err != nil || strings.TrimSpace(string(uid)) != "0" {
			t.Fatalf("administrative scriptlet UID: %q %v", uid, err)
		}
	}
	verifyVersion("1")
	// Also verifies that a root caller's read is demoted and that its cache is
	// invalidated after the subsequent transaction.
	var updates []PackageRef
	if err := object.Call(BusName+".Software.ListNativeUpdates", 0).Store(&updates); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range updates {
		if p.Id == "lyra-query-vm-fixture" {
			found = true
		}
	}
	if !found {
		t.Fatalf("fixture update missing: %+v", updates)
	}
	transaction("UpdateAllNative")
	verifyVersion("2")
	if err := object.Call(BusName+".Software.ListNativeUpdates", 0).Store(&updates); err != nil {
		t.Fatal(err)
	}
	for _, p := range updates {
		if p.Id == "lyra-query-vm-fixture" {
			t.Fatal("stale update returned after transaction")
		}
	}
	transaction("Remove", "official", "lyra-query-vm-fixture")
	if _, err := os.Stat("/usr/share/lyra-query-vm-fixture/version"); !os.IsNotExist(err) {
		t.Fatalf("fixture not removed: %v", err)
	}
	t.Log("QUERY_PACKAGE_ADMINISTRATION_VERIFIED")
	call := object.Call(BusName+".Users.CreateUser", 0, "queryvm", "Query VM User", "DisposableVm42!", []string{}, []byte{}, false)
	if call.Err != nil {
		t.Fatalf("CreateUser: %v", call.Err)
	}
	if out, err := exec.Command("id", "queryvm").CombinedOutput(); err != nil {
		t.Fatalf("created user missing: %v %s", err, out)
	}
	call = object.Call(BusName+".Users.UpdateUser", 0, "queryvm", "Updated Query User", "", []string{}, []byte{}, false)
	if call.Err != nil {
		t.Fatalf("UpdateUser: %v", call.Err)
	}
	call = object.Call(BusName+".Users.RemoveUser", 0, "queryvm")
	if call.Err != nil {
		t.Fatalf("RemoveUser: %v", call.Err)
	}
	if out, err := exec.Command("id", "queryvm").CombinedOutput(); err == nil {
		t.Fatalf("removed user still exists: %s", out)
	}
	t.Log("QUERY_USER_ADMINISTRATION_VERIFIED")
	// Root must still be able to authorize the protected backup and boot reads.
	for _, method := range []string{"Backup.ListConfigs", "Kernel.BootStatus", "Kernel.ListBootEntries", "Logs.ListUnits"} {
		if call := object.Call(BusName+"."+method, 0); call.Err != nil {
			t.Fatalf("authorized protected read %s: %v", method, call.Err)
		}
	}
	t.Log("QUERY_PROTECTED_READ_AUTHORIZATION_VERIFIED")
}
