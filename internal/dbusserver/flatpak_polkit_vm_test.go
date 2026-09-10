package dbusserver

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/profile"
)

// The disposable VM runs a real system bus and polkitd with the packaged
// policy. Flatpak is a recording command: no network or external app is needed
// to verify authorization, transaction gating and the execution UID.
func TestFlatpakPolkitVM(t *testing.T) {
	if os.Getenv("VEGA_BACKUP_VM_TEST") != "1" {
		t.Skip("requires disposable security VM")
	}
	if _, err := os.Stat("/run/vega-backup-test-vm"); err != nil || os.Geteuid() != 0 {
		t.Fatal("disposable VM marker/root missing")
	}
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
	logPath := "/tmp/flatpak-vm.log"
	if err := os.WriteFile(logPath, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(logPath, 0o666); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/bash
if [ "$1" = list ]; then
  case " $* " in
    *' --user '*) printf 'org.example.User\tUser app\n';;
    *) printf 'org.example.System\tSystem app\n';;
  esac
  exit 0
fi
printf '%s|%s\n' "$UID" "$*" >> /tmp/flatpak-vm.log
`
	if err := os.WriteFile("/usr/bin/flatpak", []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/sbin/runuser", "--user", "alice", "--", "/usr/bin/flatpak", "list", "--user").CombinedOutput(); err != nil {
		t.Fatalf("VM user/PAM setup failed: %v: %s", err, out)
	}
	subject := exec.Command("/usr/bin/backup-tests", "-test.run=^TestPolkitSubjectVMHelper$")
	subject.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 1001, Gid: 1001}}
	input, err := subject.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := subject.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	subject.Stderr = os.Stderr
	if err := subject.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { input.Close(); subject.Wait() })
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), ":") {
		t.Fatalf("nonprivileged caller did not connect: %s", scanner.Text())
	}
	userSender := dbus.Sender(scanner.Text())
	rootSender := dbus.Sender(conn.Names()[0])
	s := &SoftwareService{activity: &Activity{}, conn: conn, profile: profile.Desktop}
	for _, action := range []string{"install", "remove", "update"} {
		if err := requirePolkit(rootSender, "org.lyraos.vega.software."+action); err != nil {
			t.Fatalf("root not authorized for %s: %v", action, err)
		}
		if err := requirePolkit(userSender, "org.lyraos.vega.software."+action); err == nil {
			t.Fatalf("nonprivileged caller authorized for %s", action)
		}
	}
	methods := []struct {
		name string
		call func(dbus.Sender, string, string) (uint32, *dbus.Error)
	}{
		{"install", s.Install}, {"uninstall", s.Remove}, {"update", s.UpdatePackage},
	}
	for _, method := range methods {
		for _, sender := range []dbus.Sender{rootSender, userSender} {
			if tx, err := method.call(sender, "flathub", "--all"); tx != 0 || err == nil {
				t.Fatalf("%s accepted an option as an ID: %d %v", method.name, tx, err)
			}
		}
		if tx, err := method.call(userSender, "flathub", "org.example.System"); tx != 0 || err == nil || err.Name != BusName+".Error.AuthorizationFailed" {
			t.Fatalf("%s bypassed system authorization: %d %v", method.name, tx, err)
		}
	}
	if log, err := os.ReadFile(logPath); err != nil || len(log) != 0 {
		t.Fatalf("denied/invalid calls executed Flatpak: %s %v", log, err)
	}
	signals := make(chan *dbus.Signal, 32)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)
	if err := conn.AddMatchSignal(dbus.WithMatchInterface(BusName+".Software"), dbus.WithMatchMember("TransactionFinished")); err != nil {
		t.Fatal(err)
	}
	wait := func(tx uint32, err *dbus.Error) {
		t.Helper()
		if tx == 0 || err != nil {
			t.Fatalf("authorized call rejected: %d %v", tx, err)
		}
		select {
		case signal := <-signals:
			if len(signal.Body) != 3 || signal.Body[0] != tx || signal.Body[1] != true {
				t.Fatalf("transaction failed: %#v", signal.Body)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("authorized transaction did not finish")
		}
	}
	for _, method := range methods {
		wait(method.call(rootSender, "flathub", "org.example.System"))
	}
	wait(s.Remove(userSender, "flathub", "org.example.User"))
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"0|install -y --noninteractive --app --system -- flathub org.example.System",
		"0|uninstall -y --noninteractive --app --system -- org.example.System",
		"0|update -y --noninteractive --app --system -- org.example.System",
		"1001|uninstall -y --noninteractive --app --user -- org.example.User",
	} {
		if !strings.Contains(string(log), want+"\n") {
			t.Fatalf("scope/identity mismatch, missing %q: %s", want, log)
		}
	}
	t.Logf("real Polkit authorization and command UID/scope verified:\n%s", log)

	// Model a local authentication prompt with a VM-only challenge rule and
	// a registered D-Bus authentication agent that cancels every request.
	// The packaged policy above remains the source for the default-deny test.
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", subject.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(stat)[strings.LastIndex(string(stat), ")")+1:])
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	process := polkitVMSubject{"unix-process", map[string]dbus.Variant{
		"pid": dbus.MakeVariant(uint32(subject.Process.Pid)), "uid": dbus.MakeVariant(int32(1001)), "start-time": dbus.MakeVariant(start),
	}}
	agent := &polkitVMCancellationAgent{called: make(chan string, 4)}
	agentPath := dbus.ObjectPath("/org/lyraos/TestAuthenticationAgent")
	if err := conn.Export(agent, agentPath, "org.freedesktop.PolicyKit1.AuthenticationAgent"); err != nil {
		t.Fatal(err)
	}
	authority := conn.Object("org.freedesktop.PolicyKit1", "/org/freedesktop/PolicyKit1/Authority")
	if err := authority.Call("org.freedesktop.PolicyKit1.Authority.RegisterAuthenticationAgent", 0, process, "en_US", string(agentPath)).Err; err != nil {
		t.Fatal(err)
	}
	rule := `polkit.addRule(function(action, subject) {
  if (subject.user == "alice" && action.id.indexOf("org.lyraos.vega.software.") == 0)
    return polkit.Result.AUTH_ADMIN;
});`
	if err := os.WriteFile("/etc/polkit-1/rules.d/00-vega-vm-cancel.rules", []byte(rule), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for {
		var result struct {
			Authorized bool
			Challenge  bool
			Details    map[string]string
		}
		busSubject := polkitVMSubject{"system-bus-name", map[string]dbus.Variant{"name": dbus.MakeVariant(string(userSender))}}
		err := authority.Call("org.freedesktop.PolicyKit1.Authority.CheckAuthorization", 0, busSubject, "org.lyraos.vega.software.install", map[string]string{}, uint32(0), "").Store(&result)
		if err != nil {
			t.Fatal(err)
		}
		if result.Challenge && !result.Authorized {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("VM challenge rule was not loaded")
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, method := range methods {
		if tx, err := method.call(userSender, "flathub", "org.example.System"); tx != 0 || err == nil || err.Name != BusName+".Error.AuthorizationFailed" {
			t.Fatalf("cancelled %s started a transaction: %d %v", method.name, tx, err)
		}
		select {
		case action := <-agent.called:
			t.Logf("cancelled real Polkit prompt: %s", action)
		case <-time.After(time.Second):
			t.Fatal("authorization failed without invoking the cancellation agent")
		}
	}
	if after, err := os.ReadFile(logPath); err != nil || string(after) != string(log) {
		t.Fatalf("cancelled authorization executed Flatpak: %s %v", after, err)
	}
}

type polkitVMSubject struct {
	Kind    string
	Details map[string]dbus.Variant
}

type polkitVMCancellationAgent struct{ called chan string }

func (a *polkitVMCancellationAgent) BeginAuthentication(action, message, icon string, details map[string]string, cookie string, identities []polkitVMSubject) *dbus.Error {
	a.called <- action
	return dbus.NewError("org.freedesktop.PolicyKit1.Error.Cancelled", []interface{}{"cancelled by VM test agent"})
}

func (a *polkitVMCancellationAgent) CancelAuthentication(cookie string) *dbus.Error { return nil }

func TestPolkitSubjectVMHelper(t *testing.T) {
	if os.Getenv("VEGA_BACKUP_VM_TEST") != "1" || os.Geteuid() != 1001 {
		t.Skip("helper process for the disposable VM")
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Println(conn.Names()[0])
	io.Copy(io.Discard, os.Stdin)
}
