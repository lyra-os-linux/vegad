package distro

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func commandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func runCommandOutput(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), err
	}
	return strings.TrimSpace(string(out)), nil
}

func runCommand(name string, args ...string) error {
	_, err := runCommandOutput(name, args...)
	return err
}

func commandEnvC() []string {
	return append(os.Environ(), "LC_ALL=C")
}

// runStreamingCommand runs a subprocess and reports coarse, monotonically
// increasing progress as it emits output lines — package manager progress
// bars typically use carriage returns rather than newlines, so this can't
// track exact percentages, only "it's moving" milestones.
func runStreamingCommand(name string, args []string, report ProgressFunc, startMsg, doneMsg string) error {
	return runStreamingCmd(exec.Command(name, args...), report, startMsg, doneMsg)
}

// runStreamingCmd is runStreamingCommand's shared core, taking an
// already-built *exec.Cmd so callers that need a custom Env (e.g. apt's
// DEBIAN_FRONTEND=noninteractive) don't have to duplicate the
// streaming/progress logic.
func runStreamingCmd(cmd *exec.Cmd, report ProgressFunc, startMsg, doneMsg string) error {
	report(0, startMsg)
	name := cmd.Path

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Split(bufio.ScanLines)
	var lastLines []string
	percent := uint32(10)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lastLines = append(lastLines, line)
		if len(lastLines) > 20 {
			lastLines = lastLines[1:]
		}
		if percent < 90 {
			percent += 5
		}
		report(percent, line)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%s: %w — %s", name, err, strings.Join(lastLines, " | "))
	}
	report(100, doneMsg)
	return nil
}
