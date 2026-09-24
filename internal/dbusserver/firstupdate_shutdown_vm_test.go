package dbusserver

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPreparationStopSystemdVM(t *testing.T) {
	if os.Getenv("VEGA_PREPARATION_STOP_VM") != "1" {
		t.Skip("disposable stop VM only")
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
	for _, phase := range []string{"import", "refresh"} {
		if err := os.WriteFile("/run/stop-mode", []byte(phase), 0644); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove("/run/stop-ready")
		run("start", firstUpdateUnit)
		deadline := time.Now().Add(20 * time.Second)
		for {
			if _, err := os.Stat("/run/stop-ready"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				out, _ := exec.Command("systemctl", "status", firstUpdateUnit).CombinedOutput()
				t.Fatalf("fixture not ready: %s", out)
			}
			time.Sleep(20 * time.Millisecond)
		}
		start := time.Now()
		run("stop", firstUpdateUnit)
		if time.Since(start) > 43*time.Second {
			t.Fatal("coordinator did not finish before cgroup kill bound")
		}
		if _, err := os.Stat(firstUpdateMarkerPath()); !os.IsNotExist(err) {
			t.Fatal("completion marker after stop")
		}
		status, err := readPreparationStatus(preparationStatePath(firstUpdateMarkerPath()))
		if err != nil || status.ErrorKind != "interrupted" {
			t.Fatalf("state: %+v %v", status, err)
		}
		if phase == "import" {
			if _, err := os.Stat("/run/drained"); err != nil {
				t.Fatal("RPM-key command not allowed to drain")
			}
		}
		if phase == "refresh" {
			child, _ := os.ReadFile("/run/stubborn-child")
			stat, err := os.ReadFile("/proc/" + strings.TrimSpace(string(child)) + "/stat")
			if err == nil && !strings.Contains(string(stat), ") Z ") {
				t.Fatalf("live descendant after stop: %s", stat)
			}
		}
		t.Logf("%s: coordinated stop in %s, resumable state", phase, time.Since(start))
	}
	_ = os.Remove("/run/stop-mode")
	run("start", firstUpdateUnit)
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(firstUpdateMarkerPath()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("next invocation did not complete")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Log("next invocation completed without manual state cleanup")
}
