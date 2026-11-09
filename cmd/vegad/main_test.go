package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestHelperMain runs main() with the arguments in VEGAD_TEST_ARGS; the
// tests below re-execute the test binary so os.Exit stays in a child.
func TestHelperMain(t *testing.T) {
	args, ok := os.LookupEnv("VEGAD_TEST_ARGS")
	if !ok {
		t.Skip("helper process")
	}
	os.Args = append([]string{"vegad"}, strings.Fields(args)...)
	main()
}

// vegad#129: unknown or incomplete arguments never start a second daemon.
func TestUnknownArgumentsExitWithUsage(t *testing.T) {
	for _, args := range []string{"nonsense", "check-health x", "check-updates extra",
		"first-update extra", "backup run", "backup run a b", "backup prepare-target", "query x"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperMain$")
		cmd.Env = append(os.Environ(), "VEGAD_TEST_ARGS="+args)
		out, err := cmd.CombinedOutput()
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 2 || !strings.Contains(string(out), "uso:") {
			t.Fatalf("vegad %s: err %v, output %s", args, err, out)
		}
		if strings.Contains(string(out), "starting") {
			t.Fatalf("vegad %s started the daemon: %s", args, out)
		}
	}
}
