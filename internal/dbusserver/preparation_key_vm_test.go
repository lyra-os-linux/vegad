package dbusserver

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/distro"
)

// The VM installs a controlled zypper prompt responder; it cannot reach host repos.
func TestPreparationKeySystemdVM(t *testing.T) {
	if os.Getenv("VEGA_PREPARATION_KEY_VM") != "1" {
		t.Skip("disposable key VM only")
	}
	if _, err := os.Stat("/run/preparation-test-vm"); err != nil || os.Geteuid() != 0 {
		t.Fatal("VM guard missing")
	}
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	run("start", "polkit.service", "vegad.service")
	provider, err := distro.NewProvider(distro.OpenSUSELeap)
	if err != nil {
		t.Fatal(err)
	}
	err = provider.Package().(interface{ DiscoverPreparationKey() error }).DiscoverPreparationKey()
	var key *distro.UntrustedKeyError
	if !errors.As(err, &key) || key.Repo != "fixture" {
		t.Fatalf("discovery: %v", err)
	}
	if err := persistPreparationStatus(preparationStatePath(firstUpdateMarkerPath()), preparationFailure("refreshing", err, time.Now())); err != nil {
		t.Fatal(err)
	}
	// Discovery and approval deliberately use different processes/backend instances.
	run("restart", "vegad.service")
	helper := func(mode string) {
		t.Helper()
		cmd := exec.Command("/usr/bin/preparation-tests", "-test.run=^TestPreparationKeyVMClient$", "-test.v")
		cmd.Env = append(os.Environ(), "VEGA_KEY_CLIENT="+mode)
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 1001, Gid: 1001}}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("client %s: %v %s", mode, err, out)
		} else {
			t.Logf("%s", out)
		}
	}
	helper("denied")
	rule := `polkit.addRule(function(action,subject){if(action.id=="org.lyraos.vega.software.manage-repos" && subject.user=="alice")return polkit.Result.YES;});`
	if err := os.WriteFile("/etc/polkit-1/rules.d/10-preparation-test.rules", []byte(rule), 0644); err != nil {
		t.Fatal(err)
	}
	run("restart", "polkit.service")
	time.Sleep(time.Second)
	helper("stale")
	if _, err := os.Stat("/run/key-imported"); !os.IsNotExist(err) {
		t.Fatal("stale token imported key")
	}
	// The repository changes after the client reads the proposal, before approval.
	if err := os.WriteFile("/run/source-changed", nil, 0644); err != nil {
		t.Fatal(err)
	}
	helper("changed-source")
	if _, err := os.Stat("/run/key-imported"); !os.IsNotExist(err) {
		t.Fatal("changed source imported key")
	}
	helper("allowed")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat("/run/preparation-started"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("approval did not resume preparation")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Stat("/run/key-imported"); err != nil {
		t.Fatal("approved key was not imported")
	}
	t.Log("persistent key, denied authorization, stale token, changed source and authorized resume verified")
}

func TestPreparationKeyVMClient(t *testing.T) {
	mode := os.Getenv("VEGA_KEY_CLIENT")
	if mode == "" {
		t.Skip("VM client only")
	}
	if _, err := os.Stat("/run/preparation-test-vm"); err != nil || os.Geteuid() != 1001 {
		t.Fatal("VM guard missing")
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	object := conn.Object(BusName, ObjectPath)
	var keys []distro.PreparationKey
	if err := object.Call(BusName+".Preparation.GetPendingKeys", 0).Store(&keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Repo != "fixture" || len(keys[0].Fingerprint) != 40 || len(keys[0].Token) != 64 {
		t.Fatalf("keys: %+v", keys)
	}
	key := keys[0]
	if mode == "stale" {
		key.Token = strings.Repeat("0", 64)
	}
	if err := conn.AddMatchSignal(dbus.WithMatchInterface(BusName+".Software"), dbus.WithMatchMember("TransactionFinished")); err != nil {
		t.Fatal(err)
	}
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)
	var id uint32
	err = object.Call(BusName+".Preparation.ApproveKey", 0, key.Repo, key.Fingerprint, key.Token).Store(&id)
	if mode == "denied" {
		var denied dbus.Error
		if !errors.As(err, &denied) || denied.Name != BusName+".Error.AuthorizationFailed" {
			t.Fatalf("expected denial: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	timeout := time.After(20 * time.Second)
	for {
		select {
		case signal := <-signals:
			if signal.Name != BusName+".Software.TransactionFinished" || signal.Body[0].(uint32) != id {
				continue
			}
			success := signal.Body[1].(bool)
			if success != (mode == "allowed") {
				t.Fatalf("%s: %v", mode, signal.Body)
			}
			return
		case <-timeout:
			t.Fatal("transaction timeout")
		}
	}
}
