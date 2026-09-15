package dbusserver

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/profile"
)

func requireQueryVM(t *testing.T) {
	t.Helper()
	if os.Getenv("VEGA_BACKUP_VM_TEST") != "1" {
		t.Skip("requires disposable systemd VM")
	}
	if _, err := os.Stat("/run/vega-backup-test-vm"); err != nil || os.Geteuid() != 0 {
		t.Fatal("VM marker/root missing")
	}
}

func TestQueryHardeningSystemdVM(t *testing.T) {
	requireQueryVM(t)
	if out, err := exec.Command("zypper", "--non-interactive", "refresh").CombinedOutput(); err != nil {
		t.Fatalf("fixture metadata: %v %s", err, out)
	}
	caller := &desktopUser{Uid: 1001, Gid: 1001, Username: "alice", HomeDir: "/home/alice", RuntimeDir: "/run/user/1001"}
	for _, path := range []string{caller.HomeDir, caller.HomeDir + "/.cache", caller.HomeDir + "/.local", caller.HomeDir + "/.local/share", caller.HomeDir + "/.local/share/flatpak", caller.RuntimeDir} {
		if err := os.Chown(path, 1001, 1001); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll("/var/lib/query-owned", 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown("/var/lib/query-owned", 1001, 1001); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/root/query-secret", []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/tmp/query-host-secret", []byte("private tmp must hide this"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, u := range []*desktopUser{caller, nil} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cmd, err := queryCommand(ctx, u, false, profile.Server)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		cmd.Args = append(cmd.Args[:len(cmd.Args)-2], "/usr/bin/backup-tests", "-test.run=^TestQueryConfinedVMHelper$", "-test.v")
		// This helper has the exact production unit properties; only the executable
		// is replaced to observe the restrictions, never to launch host operations.
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil || !strings.Contains(string(out), "QUERY_PROFILE_VERIFIED") {
			t.Fatalf("confined probe: %v\n%s", err, out)
		}
		t.Logf("profile caller=%v: %s", u, out)
	}
	// A direct root invocation of the worker is forbidden, including valid reads.
	cmd := exec.Command("/usr/lib/vega/vegad", "query")
	cmd.Stdin = strings.NewReader(`{"Interface":"System","Method":"Ping","Args":[]}`)
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "refuses root") {
		t.Fatalf("root worker accepted: %v %s", err, out)
	}
	if out, err := exec.Command("systemctl", "start", "vegad.service").CombinedOutput(); err != nil {
		t.Fatalf("packaged daemon: %v %s", err, out)
	}
	t.Cleanup(func() { exec.Command("systemctl", "stop", "vegad.service").Run() })
	// Exercises actual D-Bus sender credentials, full methods, JSON round-trip,
	// the real systemd launcher, and the packaged unprivileged worker binary.
	client := exec.Command("/usr/bin/backup-tests", "-test.run=^TestQueryClientVMHelper$", "-test.v")
	client.Env = append(os.Environ(), "HOME=/home/alice", "XDG_RUNTIME_DIR=/run/user/1001")
	client.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 1001, Gid: 1001}}
	if out, err := client.CombinedOutput(); err != nil {
		t.Fatalf("D-Bus ordinary user: %v\n%s", err, out)
	} else {
		t.Logf("D-Bus ordinary user:\n%s", out)
	}
	// A root client also gets a dedicated non-root worker for public queries.
	reply, err := executeQuery(queryRequest{Interface: "Metadata", Method: "Profile"}, nil, false, profile.Server)
	if err != nil || reply.Error != nil || len(reply.Values) != 1 || string(reply.Values[0]) != `"server"` {
		out, _ := exec.Command("journalctl", "--no-pager", "-u", "dbus.service").CombinedOutput()
		t.Fatalf("root caller query: %+v %v; bus journal: %s", reply, err, out)
	}
	// Exporter migration from the previous root-owned directory and cursor.
	for _, path := range []string{"/var/log/vega"} {
		if err := os.MkdirAll(path, 0750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile("/var/log/vega/vegad.log", []byte("old log retained\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("systemctl", "start", "vegad-log-export.service").CombinedOutput(); err != nil {
		journal, _ := exec.Command("journalctl", "--no-pager", "-u", "vegad-log-export.service").CombinedOutput()
		state, _ := exec.Command("systemctl", "show", "vegad-log-export.service", "-p", "Result", "-p", "ExecMainStatus").CombinedOutput()
		t.Fatalf("log exporter: %v %s; state: %s; journal: %s", err, out, state, journal)
	}
	data, err := os.ReadFile("/var/log/vega/vegad.log")
	if err != nil || !strings.HasPrefix(string(data), "old log retained\n") {
		t.Fatalf("old log lost: %q %v", data, err)
	}
	stat, err := os.Stat("/var/log/vega/vegad.log")
	account, lookupErr := user.Lookup("vegad-log")
	if lookupErr != nil {
		t.Fatal(lookupErr)
	}
	logUID, parseErr := strconv.ParseUint(account.Uid, 10, 32)
	if parseErr != nil || logUID == 0 || err != nil || stat.Sys().(*syscall.Stat_t).Uid != uint32(logUID) {
		t.Fatalf("old log owner not migrated: %v %v", stat, err)
	}
	t.Log("QUERY_LOG_MIGRATION_VERIFIED")
}

func TestQueryLauncherCleanupSystemdVM(t *testing.T) {
	requireQueryVM(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, err := queryCommand(ctx, nil, false, profile.Server)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Args = append(cmd.Args[:len(cmd.Args)-2], "/usr/bin/backup-tests", "-test.run=^TestQueryLingeringVMHelper$", "-test.v")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); stopQueryUnit(cmd) })
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(out)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "QUERY_LINGER_READY") {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("worker did not start")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("worker startup timed out")
	}
	// Kill the launcher while its service and a child process are still alive.
	// The production cleanup must stop their complete cgroup, not just the CLI.
	cancel()
	_ = cmd.Wait()
	stopQueryUnit(cmd)
	for _, arg := range cmd.Args {
		if strings.HasPrefix(arg, "--unit=") {
			state, _ := exec.Command("systemctl", "show", strings.TrimPrefix(arg, "--unit="), "-p", "ActiveState", "--value").Output()
			if status := strings.TrimSpace(string(state)); status != "inactive" && status != "failed" {
				t.Fatalf("worker survived launcher cleanup: %q", state)
			}
		}
	}
	t.Log("QUERY_LAUNCHER_CGROUP_CLEANUP_VERIFIED")
}

func TestQueryLingeringVMHelper(t *testing.T) {
	if _, err := os.Stat("/run/vega-backup-test-vm"); err != nil || os.Geteuid() == 0 || os.Getenv("VEGAD_PROFILE") != "server" {
		t.Skip("confined VM helper only")
	}
	child := exec.Command("/usr/bin/sleep", "120")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	fmt.Println("QUERY_LINGER_READY")
	time.Sleep(120 * time.Second)
}

func TestQueryConfinedVMHelper(t *testing.T) {
	// Only the VM parent selects this helper; host unit tests never execute it.
	_, markerErr := os.Stat("/run/vega-backup-test-vm")
	if markerErr != nil || os.Geteuid() == 0 || os.Getenv("VEGAD_PROFILE") != "server" {
		t.Skip("confined VM helper only")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"CapEff:\t0000000000000000", "CapBnd:\t0000000000000000", "NoNewPrivs:\t1"} {
		if !strings.Contains(string(status), field) {
			t.Fatalf("missing %s\n%s", field, status)
		}
	}
	if _, err := os.ReadFile("/root/query-secret"); err == nil {
		t.Fatal("read root secret")
	}
	if _, err := os.Stat("/tmp/query-host-secret"); err == nil {
		t.Fatal("host tmp exposed")
	}
	if err := os.WriteFile("/var/lib/query-owned/escape", []byte("bad"), 0600); err == nil {
		t.Fatal("wrote system path")
	}
	if err := os.WriteFile("/tmp/query-private", []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 1001 {
		if os.Getenv("HOME") != "/home/alice" {
			t.Fatal("wrong home")
		}
		if err := os.WriteFile("/home/alice/.cache/query-probe", []byte("ok"), 0600); err != nil {
			t.Fatalf("caller cache unavailable: %v", err)
		}
	}
	fmt.Printf("QUERY_PROFILE_VERIFIED uid=%d gid=%d caps=0 nnp=1\n", os.Geteuid(), os.Getegid())
}

func TestQueryClientVMHelper(t *testing.T) {
	if os.Getenv("VEGA_BACKUP_VM_TEST") != "1" || os.Geteuid() != 1001 {
		t.Skip("VM ordinary caller only")
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	object := conn.Object(BusName, ObjectPath)
	for _, method := range []string{"System.Ping", "System.Distro", "System.DiskUsage", "Metadata.Profile", "Monitor.Metrics", "Monitor.ListProcesses", "Software.ListNativeInstalled", "Software.ListNativeUpdates", "Software.GetUpdateStatus", "Users.ListUsers", "Users.ListGroups"} {
		start := time.Now()
		call := object.Call(BusName+"."+method, 0)
		if call.Err != nil {
			t.Fatalf("%s: %v", method, call.Err)
		}
		if method == "Metadata.Profile" && (len(call.Body) != 1 || call.Body[0] != "server") {
			t.Fatalf("active profile lost: %+v", call.Body)
		}
		t.Logf("%s passed (%s)", method, time.Since(start))
	}
	call := object.Call(BusName+".Software.SearchNative", 0, "lyra-query-vm-fixture")
	var packages []PackageRef
	if err := call.Store(&packages); err != nil || len(packages) == 0 {
		t.Fatalf("package search as caller: %v %+v", err, packages)
	}
	t.Log("QUERY_PACKAGE_SEARCH_VERIFIED")
	// Protected reads and administrative writes must fail for a caller without
	// a Polkit agent/authorization; no privileged work may be started silently.
	for _, method := range []string{"Backup.ListConfigs", "Kernel.BootStatus", "Kernel.ListBootEntries", "Snapshots.ListSnapshots", "Logs.ListUnits"} {
		call := object.Call(BusName+"."+method, 0)
		if call.Err == nil {
			t.Fatalf("protected read authorized silently: %s", method)
		}
		t.Logf("%s denied: %v", method, call.Err)
	}
	call = object.Call(BusName+".Software.Install", 0, "official", "bash")
	if call.Err == nil {
		t.Fatal("package mutation authorized silently")
	}
	t.Log("QUERY_CALLER_AUTHORIZATION_VERIFIED")
}
