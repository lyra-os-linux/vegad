package distro

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPreparationCommandsStopAndDrain(t *testing.T) {
	for _, stubborn := range []bool{false, true} {
		t.Run(strconv.FormatBool(stubborn), func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			script := `echo ready > "$1/ready"; sleep 0.2; touch "$1/finished"`
			if stubborn {
				script = `trap '' TERM; sh -c 'trap "" TERM; while :; do sleep 1; done' & echo $! > "$1/child"; echo ready > "$1/ready"; wait`
			}
			cmd := exec.Command("/bin/sh", "-c", script, "fixture", dir)
			done := make(chan error, 1)
			go func() { _, err := drainCommand(ctx, cmd, time.Second, 50*time.Millisecond); done <- err }()
			deadline := time.Now().Add(3 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child not ready")
				}
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("stop exceeded bound")
			}
			if !stubborn {
				if _, err := os.Stat(filepath.Join(dir, "finished")); err != nil {
					t.Fatal("current command did not drain")
				}
			} else {
				data, _ := os.ReadFile(filepath.Join(dir, "child"))
				pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
				// Some test containers do not promptly reap adopted zombies; none may run.
				stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
				if err == nil && !strings.Contains(string(stat), ") Z ") {
					t.Fatalf("live descendant survived: %s", stat)
				}
			}
		})
	}
}
func TestPreparationPrecancelledCommandNeverStarts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if _, err := preparationCommandOutput(ctx, cmd); !errors.Is(err, context.Canceled) || cmd.Process != nil {
		t.Fatalf("started cancelled command: %v", err)
	}
}
