package diagnostics

import (
	"strings"
)

// Tool is an executable a test needs, and the package that ships it.
type Tool struct {
	Command string
	Package string
}

// Test describes one health test. Run is nil until the test is implemented.
type Test struct {
	ID               string
	Tools            []Tool
	EstimatedSeconds uint32
	RequiresAC       bool
	NeedsBtrfs       bool
	Run              TestFunc
}

// Tests lists every test in the order a full run executes them: quick reads
// first, so a later safeguard abort still leaves useful results.
var Tests = []Test{
	{ID: "packages", Tools: []Tool{{"rpm", "rpm"}}, EstimatedSeconds: 120, Run: runPackages},
	{ID: "disk-health", Tools: []Tool{{"smartctl", "smartmontools"}}, EstimatedSeconds: 10, Run: runDiskHealth},
	{ID: "logs", Tools: []Tool{{"journalctl", "systemd"}}, EstimatedSeconds: 20, Run: runLogs},
	{ID: "platform", EstimatedSeconds: 30, Run: runPlatform},
	{ID: "fs-scrub", Tools: []Tool{{"btrfs", "btrfsprogs"}}, EstimatedSeconds: 120, NeedsBtrfs: true},
	{ID: "cpu-stress", Tools: []Tool{{"stress-ng", "stress-ng"}}, EstimatedSeconds: 240, RequiresAC: true},
	{ID: "memory-stress", Tools: []Tool{{"stress-ng", "stress-ng"}}, EstimatedSeconds: 240, RequiresAC: true},
	{ID: "disk-bench", Tools: []Tool{{"fio", "fio"}}, EstimatedSeconds: 120, RequiresAC: true},
}

// Lookup returns the test with id.
func Lookup(id string) (Test, bool) {
	for _, test := range Tests {
		if test.ID == id {
			return test, true
		}
	}
	return Test{}, false
}

// Availability returns whether the test can run here and, if not, the stable
// reason identifier from the contract (missing-tool:<package>, not-btrfs,
// not-implemented).
func (t Test) Availability(env *Env) (bool, string) {
	if t.Run == nil {
		return false, "not-implemented"
	}
	for _, tool := range t.Tools {
		if _, err := env.LookPath(tool.Command); err != nil {
			return false, "missing-tool:" + tool.Package
		}
	}
	if t.NeedsBtrfs && !rootIsBtrfs(env) {
		return false, "not-btrfs"
	}
	return true, ""
}

func rootIsBtrfs(env *Env) bool {
	data, err := env.ReadFile("/proc/self/mounts")
	if err != nil {
		return false
	}
	return mountType(string(data), "/") == "btrfs"
}

// mountType returns the filesystem type of the last mount at target.
func mountType(mounts, target string) string {
	fsType := ""
	for _, line := range strings.Split(mounts, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[1] == target {
			fsType = fields[2]
		}
	}
	return fsType
}
