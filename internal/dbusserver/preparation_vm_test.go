package dbusserver

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestPreparationSystemdVM(t *testing.T) {
	if os.Getenv("VEGA_PREPARATION_VM") != "1" {
		t.Skip("requires disposable preparation VM")
	}
	if _, err := os.Stat("/run/preparation-test-vm"); err != nil || os.Geteuid() != 0 {
		t.Fatal("VM guard missing")
	}
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			t.Fatalf("systemctl %v: %v %s", args, err, out)
		}
	}
	run("start", "polkit.service", "vegad.service")
	helper := func(mode string) {
		t.Helper()
		cmd := exec.Command("/usr/bin/preparation-tests", "-test.run=^TestPreparationVMClient$", "-test.v")
		cmd.Env = append(os.Environ(), "VEGA_PREPARATION_CLIENT="+mode)
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 1001, Gid: 1001}}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("client %s: %v\n%s", mode, err, out)
		} else {
			t.Logf("%s: %s", mode, out)
		}
	}
	helper("denied")
	if _, err := os.Stat("/run/preparation-started"); !os.IsNotExist(err) {
		t.Fatal("denied retry started service")
	}
	rule := `polkit.addRule(function(action, subject) { if (action.id == "org.lyraos.vega.software.manage-repos" && subject.user == "alice") return polkit.Result.YES; });`
	if err := os.WriteFile("/etc/polkit-1/rules.d/10-preparation-test.rules", []byte(rule), 0644); err != nil {
		t.Fatal(err)
	}
	run("restart", "polkit.service")
	time.Sleep(time.Second)
	helper("allowed")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat("/run/preparation-started"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("authorized retry did not start service")
		}
		time.Sleep(100 * time.Millisecond)
	}
	before, err := exec.Command("systemctl", "show", firstUpdateUnit, "--property=MainPID", "--value").Output()
	if err != nil {
		t.Fatal(err)
	}
	helper("running")
	after, err := exec.Command("systemctl", "show", firstUpdateUnit, "--property=MainPID", "--value").Output()
	if err != nil || string(before) != string(after) || strings.TrimSpace(string(after)) == "0" {
		t.Fatalf("running process changed: %s -> %s, %v", before, after, err)
	}
	run("stop", firstUpdateUnit)
	// A failed unit must also recover: this exercises the reset-failed branch.
	dropin := "/etc/systemd/system/vegad-first-update.service.d/fail.conf"
	if err := os.MkdirAll(filepath.Dir(dropin), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dropin, []byte("[Service]\nExecStart=\nExecStart=/bin/bash -c 'exit 1'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("daemon-reload")
	_ = exec.Command("systemctl", "start", firstUpdateUnit).Run()
	deadline = time.Now().Add(5 * time.Second)
	for exec.Command("systemctl", "is-failed", "--quiet", firstUpdateUnit).Run() != nil {
		if time.Now().After(deadline) {
			t.Fatal("fixture did not enter failed state")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := os.Remove(dropin); err != nil {
		t.Fatal(err)
	}
	run("daemon-reload")
	helper("allowed")
	run("stop", firstUpdateUnit)
	status := PreparationStatus{State: "failed", Phase: "refreshing", ErrorKind: "network", LastError: "Verifique a conexão de rede.", UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := persistPreparationStatus(preparationStatePath(firstUpdateMarkerPath()), status); err != nil {
		t.Fatal(err)
	}
	run("restart", "vegad.service")
	helper("persisted")
	t.Log("authorized retry, rejected duplicate, unprivileged query and persisted failure verified")
}

func TestPreparationVMClient(t *testing.T) {
	mode := os.Getenv("VEGA_PREPARATION_CLIENT")
	if mode == "" {
		t.Skip("VM client subprocess only")
	}
	if _, err := os.Stat("/run/preparation-test-vm"); err != nil {
		t.Fatal("VM guard missing")
	}
	if os.Geteuid() != 1001 {
		t.Fatal("client must be ordinary user")
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	object := conn.Object(BusName, ObjectPath)
	var status PreparationStatus
	if err := object.Call(BusName+".Preparation.GetStatus", 0).Store(&status); err != nil {
		t.Fatal(err)
	}
	if mode == "persisted" {
		if status.State != "failed" || status.ErrorKind != "network" || !status.CanRetry {
			t.Fatalf("persisted status: %+v", status)
		}

		return
	}
	err = object.Call(BusName+".Preparation.Retry", 0).Err
	switch mode {
	case "denied":
		var denied dbus.Error
		if !errors.As(err, &denied) || denied.Name != BusName+".Error.AuthorizationFailed" {
			t.Fatalf("expected authorization denial, got %T: %v", err, err)
		}
	case "allowed":
		if err != nil {
			t.Fatal(err)
		}
	case "running":
		if status.State != "running" || status.CanRetry || err == nil {
			t.Fatalf("running retry: %+v %v", status, err)
		}
	default:
		t.Fatal("unknown helper mode")
	}
}
