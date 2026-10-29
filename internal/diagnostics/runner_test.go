package diagnostics

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecRunnerReturnsExitCodesAndCLocale(t *testing.T) {
	out, code, err := ExecRunner(context.Background(), "sh", "-c", "echo $LC_ALL; exit 3")
	if err != nil || code != 3 || strings.TrimSpace(string(out)) != "C" {
		t.Fatalf("out=%q code=%d err=%v", out, code, err)
	}
	if _, _, err := ExecRunner(context.Background(), "/nonexistent/tool"); err == nil {
		t.Fatal("missing tool must be an error")
	}
}

func TestExecRunnerCancelKillsTheWholeProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := ExecRunner(ctx, "sh", "-c", "sleep 60 & echo $! > "+pidFile+"; wait")
		done <- err
	}()
	var child int
	for i := 0; i < 100 && child == 0; i++ {
		time.Sleep(20 * time.Millisecond)
		data, _ := os.ReadFile(pidFile)
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}
	if child == 0 {
		t.Fatal("child never started")
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runner did not return after cancel")
	}
	time.Sleep(100 * time.Millisecond)
	if err := syscall.Kill(child, 0); err == nil {
		_ = syscall.Kill(child, syscall.SIGKILL)
		t.Fatal("grandchild survived the cancel")
	}
}
