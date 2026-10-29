package diagnostics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// hostFiles is a mutable fake sysfs/procfs, so a test can unplug the charger
// or heat the CPU while a load test runs.
type hostFiles struct {
	mu    sync.Mutex
	files map[string]string
}

func (h *hostFiles) set(path, content string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[path] = content
}

func (h *hostFiles) read(path string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if content, ok := h.files[path]; ok {
		return []byte(content), nil
	}
	return nil, os.ErrNotExist
}

func (h *hostFiles) glob(pattern string) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var matches []string
	for path := range h.files {
		if ok, _ := filepath.Match(pattern, path); ok {
			matches = append(matches, path)
		}
	}
	return matches, nil
}

// laptop is the workstation at rest: coretemp with Tjmax 100 °C, charger
// online, 20 GiB available, 12 CPUs.
func laptop() *hostFiles {
	return &hostFiles{files: map[string]string{
		"/sys/class/power_supply/ACAD/type":                                    "Mains\n",
		"/sys/class/power_supply/ACAD/online":                                  "1\n",
		"/sys/class/power_supply/BAT1/type":                                    "Battery\n",
		"/sys/class/hwmon/hwmon5/name":                                         "coretemp\n",
		"/sys/class/hwmon/hwmon5/temp1_input":                                  "80000\n",
		"/sys/class/hwmon/hwmon5/temp1_label":                                  "Package id 0\n",
		"/sys/class/hwmon/hwmon5/temp1_crit":                                   "100000\n",
		"/sys/class/hwmon/hwmon5/temp2_input":                                  "78000\n",
		"/sys/class/hwmon/hwmon5/temp2_label":                                  "Core 0\n",
		"/sys/class/hwmon/hwmon1/name":                                         "nvme\n",
		"/sys/class/hwmon/hwmon1/temp1_input":                                  "40850\n",
		"/sys/class/hwmon/hwmon1/temp1_label":                                  "Composite\n",
		"/proc/cpuinfo":                                                        "cpu MHz\t\t: 3600.000\ncpu MHz\t\t: 3400.000\n",
		"/proc/loadavg":                                                        "11.80 6.00 2.00 13/900 4242\n",
		"/proc/meminfo":                                                        "MemTotal:       32000000 kB\nMemAvailable:   20971520 kB\n",
		"/sys/devices/system/cpu/cpu0/thermal_throttle/core_throttle_count":    "0\n",
		"/sys/devices/system/cpu/cpu0/thermal_throttle/package_throttle_count": "0\n",
		"/sys/devices/system/cpu/cpu0/cpufreq/base_frequency":                  "2600000\n",
	}}
}

type recorder struct {
	mu      sync.Mutex
	samples []Sample
	calls   []string
}

func heavyEnv(t *testing.T, host *hostFiles, run Runner) (*Env, *recorder) {
	t.Helper()
	rec := &recorder{}
	return &Env{
		Locale: "en-US", DurationSeconds: 1, MemoryPercent: 70,
		Run: func(ctx context.Context, name string, args ...string) ([]byte, int, error) {
			rec.mu.Lock()
			rec.calls = append(rec.calls, strings.Join(append([]string{name}, args...), " "))
			rec.mu.Unlock()
			return run(ctx, name, args...)
		},
		ReadFile:       host.read,
		Glob:           host.glob,
		LookPath:       func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Statfs:         func(string) (uint64, uint64, error) { return 512 * gib, 400 * gib, nil },
		NumCPU:         func() int { return 12 },
		WorkDir:        filepath.Join(t.TempDir(), "vega-diagnostics"),
		SampleInterval: 10 * time.Millisecond,
		Sample: func(s Sample) {
			rec.mu.Lock()
			rec.samples = append(rec.samples, s)
			rec.mu.Unlock()
		},
	}, rec
}

// answerAfter returns output after a short load, like a 1 s stress-ng run.
func answerAfter(out []byte, code int) Runner {
	return func(ctx context.Context, name string, args ...string) ([]byte, int, error) {
		select {
		case <-time.After(60 * time.Millisecond):
			return out, code, nil
		case <-ctx.Done():
			return nil, -1, ctx.Err()
		}
	}
}

// untilCancelled is a load that never ends on its own.
func untilCancelled(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	<-ctx.Done()
	return nil, -1, ctx.Err()
}

func TestParseStressNGOnTheWorkstation(t *testing.T) {
	cpu := parseStressNG(string(fixture(t, "stress-cpu.txt")))
	if cpu.passed != 12 || cpu.failed != 0 || cpu.bogoOps != 3722319 {
		t.Fatalf("cpu %+v", cpu)
	}
	vm := parseStressNG(string(fixture(t, "stress-vm.txt")))
	if vm.passed != 4 || vm.failed != 0 || vm.bogoOps != 91864290 {
		t.Fatalf("vm %+v", vm)
	}
}

func TestCPUStressPassesAndStreamsTelemetry(t *testing.T) {
	env, rec := heavyEnv(t, laptop(), answerAfter(fixture(t, "stress-cpu.txt"), 0))
	result := runCPUStress(context.Background(), env, noProgress)
	if result.Verdict != Ok {
		t.Fatalf("verdict %s %+v", result.Verdict, result.Findings)
	}
	if !strings.HasPrefix(rec.calls[0], "nice -n 10 stress-ng --cpu 12 --cpu-method all --verify -t 1s") || !strings.Contains(rec.calls[0], "--stdout") {
		t.Fatalf("command %q", rec.calls[0])
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.samples) == 0 || *rec.samples[0].PkgTempC != 80 || *rec.samples[0].AvgMHz != 3500 || *rec.samples[0].NvmeTempC != 40 {
		t.Fatalf("samples %+v", rec.samples)
	}
	if result.Metrics["peakPkgTempC"] != 80 || result.Metrics["passed"] != 12 {
		t.Fatalf("metrics %+v", result.Metrics)
	}
}

func TestCPUStressVerificationFailureFails(t *testing.T) {
	out := strings.Replace(string(fixture(t, "stress-cpu.txt")), "failed: 0", "failed: 1", 1)
	env, _ := heavyEnv(t, laptop(), answerAfter([]byte(out), 2))
	result := runCPUStress(context.Background(), env, noProgress)
	if result.Verdict != Fail || findingCodes(result.Findings)["cpu.verify-failed"].Severity != SeverityFail {
		t.Fatalf("verdict %s %+v", result.Verdict, result.Findings)
	}
}

func TestThrottlingAloneKeepsAPassedTestHealthy(t *testing.T) {
	host := laptop()
	env, _ := heavyEnv(t, host, func(ctx context.Context, name string, args ...string) ([]byte, int, error) {
		host.set("/sys/devices/system/cpu/cpu0/thermal_throttle/core_throttle_count", "2100\n")
		return answerAfter(fixture(t, "stress-vm.txt"), 0)(ctx, name, args...)
	})
	result := runMemoryStress(context.Background(), env, noProgress)
	if result.Verdict != Ok || findingCodes(result.Findings)["cpu.throttled"].Severity != Info {
		t.Fatalf("verdict %s %+v", result.Verdict, result.Findings)
	}
}

func TestSlowLoadIsAttention(t *testing.T) {
	host := laptop()
	host.set("/proc/cpuinfo", "cpu MHz\t\t: 1000.000\n")
	env, _ := heavyEnv(t, host, answerAfter(fixture(t, "stress-cpu.txt"), 0))
	result := runCPUStress(context.Background(), env, noProgress)
	if result.Verdict != Attention || findingCodes(result.Findings)["cpu.slow-under-load"].Severity != SeverityAttention {
		t.Fatalf("verdict %s %+v", result.Verdict, result.Findings)
	}
}

func TestThermalSafeguardAbortsTheLoad(t *testing.T) {
	host := laptop()
	host.set("/sys/class/hwmon/hwmon5/temp1_input", "96000\n") // Tjmax 100 − 5 = 95
	env, _ := heavyEnv(t, host, untilCancelled)
	done := make(chan StageResult, 1)
	go func() { done <- runCPUStress(context.Background(), env, noProgress) }()
	select {
	case result := <-done:
		if result.Verdict != Aborted || findingCodes(result.Findings)["thermal.abort"].Code == "" {
			t.Fatalf("verdict %s %+v", result.Verdict, result.Findings)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hot CPU was not stopped")
	}
}

func TestOneHotSampleDoesNotAbort(t *testing.T) {
	host := laptop()
	calls := 0
	var mu sync.Mutex
	env, _ := heavyEnv(t, host, answerAfter(fixture(t, "stress-cpu.txt"), 0))
	env.Sample = func(Sample) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		// Alternate hot and cool: never two hot samples in a row.
		if calls%2 == 1 {
			host.set("/sys/class/hwmon/hwmon5/temp1_input", "96000\n")
		} else {
			host.set("/sys/class/hwmon/hwmon5/temp1_input", "80000\n")
		}
	}
	if result := runCPUStress(context.Background(), env, noProgress); result.Verdict == Aborted {
		t.Fatal("a single hot sample aborted the test")
	}
}

func TestUnpluggingTheChargerAbortsTheLoad(t *testing.T) {
	host := laptop()
	env, _ := heavyEnv(t, host, func(ctx context.Context, name string, args ...string) ([]byte, int, error) {
		host.set("/sys/class/power_supply/ACAD/online", "0\n")
		return untilCancelled(ctx, name, args...)
	})
	result := runCPUStress(context.Background(), env, noProgress)
	if result.Verdict != Aborted || findingCodes(result.Findings)["power.abort"].Code == "" {
		t.Fatalf("verdict %s %+v", result.Verdict, result.Findings)
	}
}

func TestUserCancelEndsTheLoad(t *testing.T) {
	env, _ := heavyEnv(t, laptop(), untilCancelled)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	if result := runCPUStress(ctx, env, noProgress); result.Verdict != Cancelled {
		t.Fatalf("verdict %s", result.Verdict)
	}
}

func TestPreflightSafeguards(t *testing.T) {
	host := laptop()
	env, _ := heavyEnv(t, host, untilCancelled)
	cpu, _ := Lookup("cpu-stress")
	memory, _ := Lookup("memory-stress")
	bench, _ := Lookup("disk-bench")
	logs, _ := Lookup("logs")
	if err := cpu.Check(env); err != nil {
		t.Fatalf("on AC: %v", err)
	}
	host.set("/sys/class/power_supply/ACAD/online", "0\n")
	var preflight *PreflightError
	if err := cpu.Check(env); !errors.As(err, &preflight) || preflight.Kind != "on-battery" {
		t.Fatalf("on battery: %v", err)
	}
	if err := logs.Check(env); err != nil {
		t.Fatalf("logs do not need AC: %v", err)
	}
	// A desktop or VM without any mains supply counts as plugged in.
	desktop := &hostFiles{files: map[string]string{"/proc/meminfo": "MemAvailable: 1048576 kB\n"}}
	env, _ = heavyEnv(t, desktop, untilCancelled)
	if err := memory.Check(env); !errors.As(err, &preflight) || preflight.Kind != "insufficient-resources" {
		t.Fatalf("1 GiB available: %v", err)
	}
	env.Statfs = func(string) (uint64, uint64, error) { return 64 * gib, 8 * gib, nil }
	if err := bench.Check(env); !errors.As(err, &preflight) || preflight.Kind != "insufficient-resources" {
		t.Fatalf("8 GiB free: %v", err)
	}
}

func TestMemoryStressSizesWorkersFromMemAvailable(t *testing.T) {
	env, rec := heavyEnv(t, laptop(), answerAfter(fixture(t, "stress-vm.txt"), 0))
	env.MemoryPercent = 50
	workers, perWorker := memoryWorkers(env)
	if workers != 4 || perWorker != 20*gib/2/4 {
		t.Fatalf("workers %d bytes %d", workers, perWorker)
	}
	if result := runMemoryStress(context.Background(), env, noProgress); result.Verdict != Ok {
		t.Fatalf("verdict %s %+v", result.Verdict, result.Findings)
	}
	if !strings.Contains(rec.calls[0], "--vm 4 --vm-bytes 2684354560 --vm-method all --verify") {
		t.Fatalf("command %q", rec.calls[0])
	}
	env.NumCPU = func() int { return 1 }
	if workers, _ := memoryWorkers(env); workers != 1 {
		t.Fatalf("single CPU: %d workers", workers)
	}
}

func TestScrubOnTheWorkstation(t *testing.T) {
	env, rec := heavyEnv(t, laptop(), answerAfter(fixture(t, "scrub.txt"), 0))
	result := runScrub(context.Background(), env, noProgress)
	if result.Verdict != Ok || result.Metrics["total"] != "91.90GiB" || result.Metrics["duration"] != "0:00:49" {
		t.Fatalf("verdict %s metrics %+v", result.Verdict, result.Metrics)
	}
	if rec.calls[0] != "btrfs scrub start -B -d -r /" {
		t.Fatalf("scrub must be read-only: %q", rec.calls[0])
	}
}

func TestScrubErrorsFailAndCancelStopsTheKernelScrub(t *testing.T) {
	out := strings.Replace(string(fixture(t, "scrub.txt")), "no errors found", "csum=3 verify=1", 1)
	env, _ := heavyEnv(t, laptop(), answerAfter([]byte(out), 3))
	result := runScrub(context.Background(), env, noProgress)
	if result.Verdict != Fail || !strings.Contains(findingCodes(result.Findings)["scrub.errors"].Text, "csum=3") {
		t.Fatalf("verdict %s %+v", result.Verdict, result.Findings)
	}
	env, rec := heavyEnv(t, laptop(), func(ctx context.Context, name string, args ...string) ([]byte, int, error) {
		if strings.Join(args, " ") == "scrub cancel /" {
			return nil, 0, nil
		}
		return untilCancelled(ctx, name, args...)
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	if result := runScrub(ctx, env, noProgress); result.Verdict != Cancelled {
		t.Fatalf("verdict %s", result.Verdict)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.calls[len(rec.calls)-1] != "btrfs scrub cancel /" {
		t.Fatalf("kernel scrub left running: %v", rec.calls)
	}
}

func TestScrubEstimateFollowsUsedSpace(t *testing.T) {
	env, _ := heavyEnv(t, laptop(), untilCancelled)
	env.Statfs = func(string) (uint64, uint64, error) { return 512 * gib, 412 * gib, nil }
	if got := scrubEstimate(env); got != 130 {
		t.Fatalf("100 GiB used: %d s", got)
	}
}

func TestParseFio(t *testing.T) {
	bench, err := parseFio(append([]byte("fio: note: warning\n"), fixture(t, "fio.json")...))
	if err != nil {
		t.Fatal(err)
	}
	if bench.seqReadMBps < 2000 || bench.randReadIOPS < 150000 || bench.qd1ReadIOPS < 10000 || bench.seqWriteMBps < 500 || bench.errors != 0 {
		t.Fatalf("%+v", bench)
	}
}

func TestDiskBenchWritesInsideItsDirectoryAndCleansUp(t *testing.T) {
	var jobFile string
	env, rec := heavyEnv(t, laptop(), nil)
	env.Run = func(ctx context.Context, name string, args ...string) ([]byte, int, error) {
		rec.mu.Lock()
		rec.calls = append(rec.calls, strings.Join(append([]string{name}, args...), " "))
		rec.mu.Unlock()
		if name == "ionice" {
			jobFile = args[len(args)-1]
			data, _ := os.ReadFile(jobFile)
			if !strings.Contains(string(data), "directory="+env.WorkDir) || !strings.Contains(string(data), "io_size=256M") {
				t.Errorf("job file %s", data)
			}
			_ = os.WriteFile(filepath.Join(env.WorkDir, "vega-bench.dat"), []byte("x"), 0o600)
			return fixture(t, "fio.json"), 0, nil
		}
		return nil, 0, nil
	}
	result := runDiskBench(context.Background(), env, noProgress)
	if result.Verdict != Ok || result.Metrics["seqReadMBps"].(int) < 2000 {
		t.Fatalf("verdict %s metrics %+v", result.Verdict, result.Metrics)
	}
	if rec.calls[0] != "chattr +C "+env.WorkDir || !strings.HasPrefix(rec.calls[1], "ionice -c3 nice -n 10 fio --output-format=json ") {
		t.Fatalf("calls %v", rec.calls)
	}
	entries, _ := os.ReadDir(env.WorkDir)
	if len(entries) != 0 {
		t.Fatalf("benchmark files left behind: %v", entries)
	}
	info, _ := os.Stat(env.WorkDir)
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("work dir mode %v", info.Mode())
	}
}

func TestCleanupWorkDirRemovesCrashLeftovers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vega-diagnostics")
	if err := CleanupWorkDir(dir); err != nil {
		t.Fatalf("absent dir: %v", err)
	}
	_ = os.MkdirAll(filepath.Join(dir, "sub"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "vega-bench.dat"), []byte("left by kill -9"), 0o600)
	if err := CleanupWorkDir(dir); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("left %v", entries)
	}
}
