package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	benchMinFree   = 10 * gib
	benchSeconds   = 75 // runtime of the timed jobs plus the writes on NVMe
	benchFileName  = "vega-bench.dat"
	benchSeqSize   = "4G"
	benchRandWrite = "256M" // per job, 4 jobs: 1 GiB; ~5 GiB written per run
)

func benchPreflight(env *Env) error {
	if env.Statfs == nil {
		return nil
	}
	dir := filepath.Dir(env.WorkDir)
	_, available, err := env.Statfs(dir)
	if err != nil {
		return &PreflightError{Kind: "insufficient-resources", Detail: err.Error()}
	}
	if available < benchMinFree {
		return &PreflightError{Kind: "insufficient-resources", Detail: fmt.Sprintf("%d GiB livres em %s < 10 GiB", available/gib, dir)}
	}
	return nil
}

// benchJobFile is the fio sequence from vegad#98: sequential write (creates
// the file), sequential read, random read and write at QD32×4, and QD1
// latency. stonewall runs the jobs one after another.
func benchJobFile(dir string) string {
	return fmt.Sprintf(`[global]
directory=%s
filename=%s
ioengine=libaio
direct=1
size=%s

[seq-write]
rw=write
bs=1M
iodepth=32
stonewall

[seq-read]
rw=read
bs=1M
iodepth=32
time_based
runtime=20
stonewall

[rand-read]
rw=randread
bs=4k
iodepth=32
numjobs=4
time_based
runtime=20
stonewall

[rand-write]
rw=randwrite
bs=4k
iodepth=32
numjobs=4
io_size=%s
stonewall

[qd1-read]
rw=randread
bs=4k
iodepth=1
time_based
runtime=15
stonewall
`, dir, benchFileName, benchSeqSize, benchRandWrite)
}

type fioReport struct {
	Jobs []struct {
		Name  string  `json:"jobname"`
		Error int     `json:"error"`
		Read  fioSide `json:"read"`
		Write fioSide `json:"write"`
	} `json:"jobs"`
}

type fioSide struct {
	BwBytes float64 `json:"bw_bytes"`
	IOPS    float64 `json:"iops"`
	LatNs   struct {
		Mean float64 `json:"mean"`
	} `json:"lat_ns"`
}

type benchResult struct {
	seqWriteMBps, seqReadMBps   float64
	randReadIOPS, randWriteIOPS float64
	qd1ReadIOPS, qd1LatencyUs   float64
	errors                      int
}

// parseFio sums the per-process entries of each job (numjobs=4 reports four).
// fio may print warnings before the JSON document.
func parseFio(out []byte) (benchResult, error) {
	var r benchResult
	if i := strings.IndexByte(string(out), '{'); i > 0 {
		out = out[i:]
	}
	var report fioReport
	if err := json.Unmarshal(out, &report); err != nil {
		return r, err
	}
	for _, job := range report.Jobs {
		if job.Error != 0 {
			r.errors++
		}
		switch job.Name {
		case "seq-write":
			r.seqWriteMBps += job.Write.BwBytes / 1e6
		case "seq-read":
			r.seqReadMBps += job.Read.BwBytes / 1e6
		case "rand-read":
			r.randReadIOPS += job.Read.IOPS
		case "rand-write":
			r.randWriteIOPS += job.Write.IOPS
		case "qd1-read":
			r.qd1ReadIOPS += job.Read.IOPS
			r.qd1LatencyUs = job.Read.LatNs.Mean / 1000
		}
	}
	return r, nil
}

// prepareWorkDir creates the root-only benchmark directory and turns off
// copy-on-write on Btrfs, so the benchmark measures the disk, not CoW.
func prepareWorkDir(ctx context.Context, env *Env) error {
	if err := os.MkdirAll(env.WorkDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(env.WorkDir, 0o700); err != nil {
		return err
	}
	_, _, _ = env.Run(ctx, "chattr", "+C", env.WorkDir)
	return nil
}

func runDiskBench(ctx context.Context, env *Env, progress Progress) StageResult {
	start := time.Now()
	result := StageResult{ID: "disk-bench", Metrics: map[string]any{}}
	if err := benchPreflight(env); err != nil {
		result.Verdict = Unavailable
		result.Summary = env.t("diagnostics.summary.low-disk")
		result.Findings = []Finding{env.finding(Info, "bench.insufficient-space", err.Error())}
		return result
	}
	if err := prepareWorkDir(ctx, env); err != nil {
		return failedToRun(env, result, "fio", err, start)
	}
	// The benchmark file never outlives the stage: done, failed or cancelled.
	defer func() { _ = CleanupWorkDir(env.WorkDir) }()
	jobFile := filepath.Join(env.WorkDir, "vega-bench.fio")
	if err := os.WriteFile(jobFile, []byte(benchJobFile(env.WorkDir)), 0o600); err != nil {
		return failedToRun(env, result, "fio", err, start)
	}

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
		out, code, err := env.Run(loadCtx, "ionice", "-c3", "nice", "-n", "10", "fio", "--output-format=json", jobFile)
		finished <- outcome{out, code, err}
	}()
	progress(0, env.t("diagnostics.progress.disk-bench"))
	ticker := time.NewTicker(time.Second)
	var run outcome
wait:
	for {
		select {
		case run = <-finished:
			break wait
		case <-ticker.C:
			percent := uint32(min(99, int(100*time.Since(start)/(benchSeconds*time.Second))))
			progress(percent, env.t("diagnostics.progress.disk-bench"))
		}
	}
	ticker.Stop()
	stop()
	result.DurationSeconds = int(time.Since(start).Seconds())
	monitor.mu.Lock()
	result.Metrics["nvmeTempBeforeC"] = monitor.nvmeBefore
	result.Metrics["nvmeTempPeakC"] = monitor.nvmePeak
	monitor.mu.Unlock()
	if monitor.abortResult(env, &result) {
		return result
	}
	if run.err != nil {
		return failedToRun(env, result, "fio", run.err, start)
	}
	bench, err := parseFio(run.out)
	if err != nil || run.code != 0 {
		result.Verdict = Attention
		result.Summary = env.t("diagnostics.summary.tool-failed", "tool", "fio")
		result.Findings = append(result.Findings, env.finding(SeverityAttention, "bench.failed", lastLines(string(run.out), 12)))
		return result
	}
	if bench.errors > 0 {
		result.Findings = append(result.Findings, env.finding(SeverityAttention, "bench.io-errors", "", "count", strconv.Itoa(bench.errors)))
	}
	result.Metrics["seqWriteMBps"] = int(bench.seqWriteMBps)
	result.Metrics["seqReadMBps"] = int(bench.seqReadMBps)
	result.Metrics["randReadIOPS"] = int(bench.randReadIOPS)
	result.Metrics["randWriteIOPS"] = int(bench.randWriteIOPS)
	result.Metrics["qd1ReadIOPS"] = int(bench.qd1ReadIOPS)
	result.Metrics["qd1LatencyUs"] = int(bench.qd1LatencyUs)
	result.Verdict = verdictFromFindings(result.Findings)
	result.Summary = env.t("diagnostics.summary.disk-bench",
		"write", strconv.Itoa(int(bench.seqWriteMBps)), "read", strconv.Itoa(int(bench.seqReadMBps)),
		"iops", strconv.Itoa(int(bench.randReadIOPS)))
	return result
}
