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
	// Estimate refines EstimatedSeconds for this host and options.
	Estimate func(env *Env) uint32
	// Preflight refuses a start the safeguards forbid (*PreflightError).
	Preflight func(env *Env) error
}

// Seconds is the expected duration on this host.
func (t Test) Seconds(env *Env) uint32 {
	if t.Estimate != nil {
		return t.Estimate(env)
	}
	return t.EstimatedSeconds
}

// Check runs the safeguards that apply before the test starts: line power
// for load tests, then the test's own resource checks.
func (t Test) Check(env *Env) error {
	if t.RequiresAC && !onLinePower(env) {
		return &PreflightError{Kind: "on-battery", Detail: t.ID}
	}
	if t.Preflight != nil {
		return t.Preflight(env)
	}
	return nil
}

// Tests lists every test in the order a full run executes them: quick reads
// first, so a later safeguard abort still leaves useful results.
var Tests = []Test{
	{ID: "packages", Tools: []Tool{{"rpm", "rpm"}}, EstimatedSeconds: 120, Run: runPackages},
	{ID: "disk-health", Tools: []Tool{{"smartctl", "smartmontools"}}, EstimatedSeconds: 10, Run: runDiskHealth},
	{ID: "logs", Tools: []Tool{{"journalctl", "systemd"}}, EstimatedSeconds: 20, Run: runLogs},
	{ID: "platform", EstimatedSeconds: 30, Run: runPlatform},
	{ID: "security", Tools: []Tool{{"ss", "iproute2"}}, EstimatedSeconds: 20, Run: runSecurity},
	{ID: "fs-scrub", Tools: []Tool{{"btrfs", "btrfsprogs"}}, EstimatedSeconds: 120, NeedsBtrfs: true, Run: runScrub, Estimate: scrubEstimate},
	{ID: "cpu-stress", Tools: []Tool{{"stress-ng", "stress-ng"}}, EstimatedSeconds: 240, RequiresAC: true, Run: runCPUStress, Estimate: stressEstimate},
	{ID: "memory-stress", Tools: []Tool{{"stress-ng", "stress-ng"}}, EstimatedSeconds: 240, RequiresAC: true, Run: runMemoryStress, Estimate: stressEstimate, Preflight: memoryPreflight},
	{ID: "disk-bench", Tools: []Tool{{"fio", "fio"}}, EstimatedSeconds: 90, RequiresAC: true, Run: runDiskBench, Preflight: benchPreflight},
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
