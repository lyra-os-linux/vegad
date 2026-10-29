package diagnostics

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// scrubRate is a conservative NVMe scrub rate (28/10: 1.88 GiB/s).
const scrubRate = 1 << 30

func scrubEstimate(env *Env) uint32 {
	if env.Statfs == nil {
		return 120
	}
	total, available, err := env.Statfs("/")
	if err != nil || total < available {
		return 120
	}
	return uint32((total-available)/scrubRate) + 30
}

type scrubSummary struct {
	status   string
	errors   []string
	duration string
	total    string
	rate     string
}

// parseScrub reads `btrfs scrub start -B -d -r` output; -d prints one block
// per device, each with its own "Error summary".
func parseScrub(out string) scrubSummary {
	var s scrubSummary
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "Status":
			if s.status == "" || value != "finished" {
				s.status = value
			}
		case "Error summary":
			if value != "no errors found" {
				s.errors = append(s.errors, value)
			}
		case "Duration":
			s.duration = value
		case "Total to scrub":
			s.total = value
		case "Rate":
			s.rate = value
		}
	}
	return s
}

// scrubbedBytes reads `btrfs scrub status -R /`.
func scrubbedBytes(out string) uint64 {
	var total uint64
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && (key == "data_bytes_scrubbed" || key == "tree_bytes_scrubbed") {
			n, _ := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
			total += n
		}
	}
	return total
}

func runScrub(ctx context.Context, env *Env, progress Progress) StageResult {
	start := time.Now()
	result := StageResult{ID: "fs-scrub", Metrics: map[string]any{}}
	var used uint64
	if env.Statfs != nil {
		if total, available, err := env.Statfs("/"); err == nil && total >= available {
			used = total - available
		}
	}
	type outcome struct {
		out  []byte
		code int
		err  error
	}
	finished := make(chan outcome, 1)
	go func() {
		// -r: read-only, never repairs. -B: stay in the foreground.
		out, code, err := env.Run(ctx, "btrfs", "scrub", "start", "-B", "-d", "-r", "/")
		finished <- outcome{out, code, err}
	}()
	progress(0, env.t("diagnostics.progress.fs-scrub"))
	interval := env.SampleInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	var run outcome
wait:
	for {
		select {
		case run = <-finished:
			break wait
		case <-ticker.C:
			if used == 0 {
				continue
			}
			status, _, err := env.Run(ctx, "btrfs", "scrub", "status", "-R", "/")
			if err == nil {
				percent := min(99, int(100*scrubbedBytes(string(status))/used))
				progress(uint32(percent), env.t("diagnostics.progress.fs-scrub"))
			}
		}
	}
	ticker.Stop()
	result.DurationSeconds = int(time.Since(start).Seconds())
	if run.err != nil {
		// Killing the foreground process may leave the kernel scrubbing.
		cancelCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, _, _ = env.Run(cancelCtx, "btrfs", "scrub", "cancel", "/")
		cancel()
		return failedToRun(env, result, "btrfs", run.err, start)
	}
	summary := parseScrub(string(run.out))
	result.Metrics["usedBytes"] = used
	result.Metrics["duration"] = summary.duration
	result.Metrics["total"] = summary.total
	result.Metrics["rate"] = summary.rate
	switch {
	case len(summary.errors) > 0:
		result.Findings = append(result.Findings, env.finding(SeverityFail, "scrub.errors", strings.TrimSpace(string(run.out)), "errors", strings.Join(summary.errors, "; ")))
	case summary.status != "" && summary.status != "finished":
		result.Findings = append(result.Findings, env.finding(SeverityAttention, "scrub.incomplete", strings.TrimSpace(string(run.out)), "status", summary.status))
	case run.code != 0:
		result.Findings = append(result.Findings, env.finding(SeverityAttention, "scrub.incomplete", strings.TrimSpace(string(run.out)), "status", "exit "+strconv.Itoa(run.code)))
	}
	result.Verdict = verdictFromFindings(result.Findings)
	result.Summary = env.t("diagnostics.summary.fs-scrub", "total", summary.total, "duration", summary.duration, "errors", strconv.Itoa(len(summary.errors)))
	return result
}
