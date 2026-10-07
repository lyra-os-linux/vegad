package parental

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestFlatpakRunArgsInDisposableVM checks the exact argv builder against the
// installed Flatpak 1.16.6 fixture. It runs unconfined and does not authorize
// or enforce parental execution.
func TestFlatpakRunArgsInDisposableVM(t *testing.T) {
	if os.Getenv("LYRA_TEST_FLATPAK_RUN_ARGS") != "1" {
		t.Skip("explicit disposable VM fixture only")
	}
	cmdline, _ := os.ReadFile("/proc/cmdline")
	serial, _ := os.ReadFile("/sys/block/vda/serial")
	enforcing, _ := os.ReadFile("/sys/fs/selinux/enforce")
	if os.Geteuid() != 0 || !strings.Contains(" "+string(cmdline)+" ", " lyra.virtualization-test=1 ") ||
		strings.TrimSpace(string(serial)) != "lyra-admission-test" || strings.TrimSpace(string(enforcing)) != "0" {
		t.Fatal("unexpected VM or SELinux mode")
	}
	account, err := user.Lookup("parentaltest")
	if err != nil || account.Uid != "1003" {
		t.Fatal("fixture account changed", err)
	}
	uid, _ := strconv.Atoi(account.Uid)
	gid, _ := strconv.Atoi(account.Gid)
	if err := os.MkdirAll("/run/user/1003", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown("/run/user/1003", uid, gid); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod("/run/user/1003", 0700); err != nil {
		t.Fatal(err)
	}
	commit := func(app string) string {
		t.Helper()
		out, err := exec.Command("/usr/bin/flatpak", "info", "--system", "--show-commit", app).CombinedOutput()
		value := strings.TrimSpace(string(out))
		if err != nil || !hash.MatchString(value) {
			t.Fatalf("fixture commit %s: %v %s", app, err, out)
		}
		return value
	}
	a, b, runtime := commit("org.lyra.TestApp"), commit("org.lyra.TestAppB"), commit("org.lyra.TestRuntime")
	for _, tc := range []struct {
		name, appID, appCommit, runtimeCommit string
		allowed                               bool
	}{
		{"a-valid", "org.lyra.TestApp", a, runtime, true},
		{"b-valid", "org.lyra.TestAppB", b, runtime, true},
		{"a-with-b-commit", "org.lyra.TestApp", b, runtime, false},
		{"a-with-wrong-runtime", "org.lyra.TestApp", a, strings.Repeat("0", 64), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installed := InstalledFlatpak{
				Ref: "app/" + tc.appID + "/x86_64/stable", Scope: "system", Installation: "system",
				Commit: tc.appCommit, RuntimeRef: "runtime/org.lyra.TestRuntime/x86_64/stable",
				RuntimeCommit: tc.runtimeCommit,
			}
			args, err := BuildFlatpakRunArgs(ResolvedFlatpak{installed}, uint32(uid))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			command := []string{"-u", "parentaltest", "--", "env", "XDG_RUNTIME_DIR=/run/user/1003",
				"dbus-run-session", "--", "/usr/bin/flatpak"}
			command = append(command, args...)
			out, err := exec.CommandContext(ctx, "/usr/sbin/runuser", command...).CombinedOutput()
			if ctx.Err() != nil || (err == nil) != tc.allowed {
				t.Fatalf("flatpak argv=%q allowed=%t err=%v output=%s", args, tc.allowed, err, out)
			}
			if !tc.allowed && !strings.Contains(string(out), "not installed") {
				t.Fatalf("unexpected denial argv=%q: %s", args, out)
			}
			t.Logf("app=%s expected=%t rc-success=%t", tc.appID, tc.allowed, err == nil)
		})
	}
}
