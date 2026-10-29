package diagnostics

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	stressPassed = regexp.MustCompile(`passed: (\d+)`)
	stressFailed = regexp.MustCompile(`failed: (\d+)`)
	stressBogo   = regexp.MustCompile(`metrc: \[\d+\] (cpu|vm)\s+(\d+)\s`)
)

type stressOutcome struct {
	passed, failed int
	bogoOps        int64
}

// parseStressNG reads stress-ng --stdout --metrics-brief output.
func parseStressNG(out string) stressOutcome {
	var o stressOutcome
	if m := stressPassed.FindStringSubmatch(out); m != nil {
		o.passed, _ = strconv.Atoi(m[1])
	}
	if m := stressFailed.FindStringSubmatch(out); m != nil {
		o.failed, _ = strconv.Atoi(m[1])
	}
	if m := stressBogo.FindStringSubmatch(out); m != nil {
		o.bogoOps, _ = strconv.ParseInt(m[2], 10, 64)
	}
	return o
}

func stressEstimate(env *Env) uint32 {
	if env.DurationSeconds > 0 {
		return uint32(env.DurationSeconds) + 10
	}
	return 250
}

func memoryPreflight(env *Env) error {
	if available := memAvailable(env); available < 2*gib {
		return &PreflightError{Kind: "insufficient-resources", Detail: "MemAvailable " + strconv.FormatUint(available>>20, 10) + " MiB < 2048 MiB"}
	}
	return nil
}

// memoryWorkers splits memoryPercent of MemAvailable, measured now, among
// min(4, nproc/2) workers.
func memoryWorkers(env *Env) (workers int, bytesPerWorker uint64) {
	workers = min(4, max(1, env.NumCPU()/2))
	percent := env.MemoryPercent
	if percent <= 0 || percent > 70 {
		percent = 70
	}
	return workers, memAvailable(env) * uint64(percent) / 100 / uint64(workers)
}

func runCPUStress(ctx context.Context, env *Env, progress Progress) StageResult {
	workers := env.NumCPU()
	args := []string{"-n", "10", "stress-ng", "--cpu", strconv.Itoa(workers), "--cpu-method", "all", "--verify",
		"-t", strconv.Itoa(env.DurationSeconds) + "s", "--metrics-brief", "--stdout"}
	result := runStress(ctx, env, progress, "cpu-stress", args)
	result.Metrics["workers"] = workers
	return result
}

func runMemoryStress(ctx context.Context, env *Env, progress Progress) StageResult {
	if err := memoryPreflight(env); err != nil {
		return StageResult{ID: "memory-stress", Verdict: Unavailable, Summary: env.t("diagnostics.summary.low-memory"), Findings: []Finding{env.finding(Info, "memory.insufficient", err.Error())}}
	}
	workers, perWorker := memoryWorkers(env)
	args := []string{"-n", "10", "stress-ng", "--vm", strconv.Itoa(workers), "--vm-bytes", strconv.FormatUint(perWorker, 10),
		"--vm-method", "all", "--verify", "-t", strconv.Itoa(env.DurationSeconds) + "s", "--metrics-brief", "--stdout"}
	result := runStress(ctx, env, progress, "memory-stress", args)
	result.Metrics["workers"] = workers
	result.Metrics["bytesPerWorker"] = perWorker
	return result
}

// runStress runs stress-ng through nice, with the load monitor watching
// temperature and power, and turns the outcome into a verdict.
func runStress(ctx context.Context, env *Env, progress Progress, id string, args []string) StageResult {
	start := time.Now()
	result := StageResult{ID: id, Metrics: map[string]any{"durationSeconds": env.DurationSeconds}}
	loadCtx, cancelLoad := context.WithCancel(ctx)
	defer cancelLoad()
	monitor, stop := startLoadMonitor(loadCtx, env, true, cancelLoad)

	type outcome struct {
		out  []byte
		code int
		err  error
	}
	finished := make(chan outcome, 1)
	go func() {
		out, code, err := env.Run(loadCtx, "nice", args...)
		finished <- outcome{out, code, err}
	}()
	progress(0, env.t("diagnostics.progress."+id))
	total := time.Duration(env.DurationSeconds) * time.Second
	ticker := time.NewTicker(time.Second)
	var run outcome
wait:
	for {
		select {
		case run = <-finished:
			break wait
		case <-ticker.C:
			if total > 0 {
				percent := uint32(min(99, int(100*time.Since(start)/total)))
				progress(percent, env.t("diagnostics.progress."+id))
			}
		}
	}
	ticker.Stop()
	stop()
	result.DurationSeconds = int(time.Since(start).Seconds())
	monitor.mu.Lock()
	result.Metrics["peakPkgTempC"] = monitor.peakPkg
	result.Metrics["peakCoreTempC"] = monitor.peakCore
	result.Metrics["throttleEvents"] = monitor.throttleEvents
	monitor.mu.Unlock()
	avg := monitor.avgMHz()
	result.Metrics["avgMHz"] = avg

	if monitor.abortResult(env, &result) {
		return result
	}
	if run.err != nil {
		return failedToRun(env, result, "stress-ng", run.err, start)
	}
	parsed := parseStressNG(string(run.out))
	result.Metrics["passed"] = parsed.passed
	result.Metrics["failed"] = parsed.failed
	result.Metrics["bogoOps"] = parsed.bogoOps
	prefix := strings.TrimSuffix(id, "-stress")
	if parsed.failed > 0 || run.code == 2 {
		result.Findings = append(result.Findings, env.finding(SeverityFail, prefix+".verify-failed", lastLines(string(run.out), 12), "count", strconv.Itoa(parsed.failed)))
	} else if run.code != 0 {
		result.Findings = append(result.Findings, env.finding(SeverityAttention, "stress.incomplete", lastLines(string(run.out), 12), "code", strconv.Itoa(run.code)))
	}
	// Throttling under sustained load is normal in laptops: a passed test
	// stays healthy and the count is only informative. Attention is kept for
	// a real loss of performance, a load frequency under half the nominal.
	if monitor.throttleEvents > 0 {
		result.Findings = append(result.Findings, env.finding(Info, "cpu.throttled", "", "count", strconv.Itoa(monitor.throttleEvents)))
	}
	if base := baseMHz(env); base > 0 && avg > 0 && avg < base/2 {
		result.Findings = append(result.Findings, env.finding(SeverityAttention, "cpu.slow-under-load", "avg "+strconv.Itoa(avg)+" MHz, base "+strconv.Itoa(base)+" MHz", "mhz", strconv.Itoa(avg), "base", strconv.Itoa(base)))
	}
	result.Verdict = verdictFromFindings(result.Findings)
	result.Summary = env.t("diagnostics.summary."+id,
		"passed", strconv.Itoa(parsed.passed), "workers", strconv.Itoa(parsed.passed+parsed.failed),
		"temp", strconv.Itoa(monitor.peakPkg), "mhz", strconv.Itoa(avg))
	return result
}

func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
