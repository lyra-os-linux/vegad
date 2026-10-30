package diagnostics

import (
	"bufio"
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The "boot" test (add-boot-analysis): how long the last boot took, phase by
// phase, and what held it. Read-only; the fixes are disable-wait-online and
// disable-service.

const (
	slowBootSeconds    = 60.0
	slowServiceSeconds = 10.0
	slowWaitOnline     = 5.0
	waitOnlineUnit     = "NetworkManager-wait-online.service"
)

var (
	durationPart = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)(min|ms|us|s|h)`)
	phasePart    = regexp.MustCompile(`([0-9][0-9.a-z ]*?) \(([a-z]+)\)`)
	graphicalAt  = regexp.MustCompile(`graphical\.target reached after (.+?) in userspace`)
	chainEntry   = regexp.MustCompile(`([A-Za-z0-9@._:\\-]+\.(?:service|mount|device|target|socket|swap)) @[0-9.a-z ]+?(?: \+([0-9.a-z ]+))?$`)
)

// parseSystemdDuration reads "1min 59.569s", "639ms", "2.082s".
func parseSystemdDuration(text string) (float64, bool) {
	matches := durationPart.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return 0, false
	}
	total := 0.0
	for _, m := range matches {
		value, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, false
		}
		switch m[2] {
		case "h":
			total += value * 3600
		case "min":
			total += value * 60
		case "s":
			total += value
		case "ms":
			total += value / 1000
		case "us":
			total += value / 1e6
		}
	}
	return total, true
}

type bootTimes struct {
	phases    map[string]float64
	total     float64
	graphical float64 // seconds into userspace; 0 if not reached
}

// parseAnalyzeTime reads "systemd-analyze time". Phases a machine does not
// have (firmware and loader in a VM, initrd without one) are simply absent.
func parseAnalyzeTime(out string) (bootTimes, bool) {
	times := bootTimes{phases: map[string]float64{}}
	first, _, _ := strings.Cut(out, "\n")
	head, total, ok := strings.Cut(first, " = ")
	if !ok || !strings.HasPrefix(head, "Startup finished in ") {
		return times, false
	}
	for _, m := range phasePart.FindAllStringSubmatch(strings.TrimPrefix(head, "Startup finished in "), -1) {
		if seconds, ok := parseSystemdDuration(m[1]); ok {
			times.phases[m[2]] = seconds
		}
	}
	times.total, _ = parseSystemdDuration(total)
	if m := graphicalAt.FindStringSubmatch(out); m != nil {
		times.graphical, _ = parseSystemdDuration(m[1])
	}
	return times, len(times.phases) > 0
}

type unitTime struct {
	Unit    string  `json:"unit"`
	Seconds float64 `json:"seconds"`
}

// parseBlame reads "systemd-analyze blame": "1min 59.569s vegad-update-check.service".
func parseBlame(out string) []unitTime {
	var units []unitTime
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		unit := fields[len(fields)-1]
		seconds, ok := parseSystemdDuration(strings.Join(fields[:len(fields)-1], " "))
		if ok && strings.Contains(unit, ".") {
			units = append(units, unitTime{unit, seconds})
		}
	}
	return units
}

// parseCriticalChain returns the "+time" of each unit on the chain to
// graphical.target (units without a "+" took no measurable time).
func parseCriticalChain(out string) map[string]float64 {
	chain := map[string]float64{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimLeft(line, " `|-└─├│")
		m := chainEntry.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		seconds := 0.0
		if m[2] != "" {
			seconds, _ = parseSystemdDuration(m[2])
		}
		chain[m[1]] = seconds
	}
	return chain
}

func showValue(ctx context.Context, env *Env, property, unit string) string {
	out, _, err := env.Run(ctx, "systemctl", "show", "-p", property, "--value", unit)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// startedBeforeLogin: the unit began starting before graphical.target was
// reached. Timer jobs that run later (an update check) do not delay the boot.
func startedBeforeLogin(ctx context.Context, env *Env, unit string, graphicalMono uint64) bool {
	if graphicalMono == 0 {
		return true
	}
	start, err := strconv.ParseUint(showValue(ctx, env, "InactiveExitTimestampMonotonic", unit), 10, 64)
	return err != nil || start == 0 || start <= graphicalMono
}

// userEnabled: enabled, but not by a preset of the image. Only these can be
// disabled by the remedy; the image's own services get guidance.
func userEnabled(ctx context.Context, env *Env, unit string) bool {
	return showValue(ctx, env, "UnitFileState", unit) == "enabled" &&
		showValue(ctx, env, "UnitFilePreset", unit) == "disabled"
}

// networkOnlineWanted lists the units, other than wait-online itself, that
// pull network-online.target in: disabling wait-online would break them.
func networkOnlineWanted(ctx context.Context, env *Env) []string {
	var units []string
	for _, unit := range strings.Fields(showValue(ctx, env, "WantedBy", "network-online.target") + " " + showValue(ctx, env, "RequiredBy", "network-online.target")) {
		if unit != waitOnlineUnit {
			units = append(units, unit)
		}
	}
	return units
}

func runBoot(ctx context.Context, env *Env, progress Progress) StageResult {
	start := time.Now()
	result := StageResult{ID: "boot", Metrics: map[string]any{}}
	var findings []Finding

	progress(10, env.t("diagnostics.progress.boot.time"))
	out, code, err := env.Run(ctx, "systemd-analyze", "time")
	if err != nil {
		return failedToRun(env, result, "systemd-analyze", err, start)
	}
	times, ok := parseAnalyzeTime(string(out))
	if code != 0 || !ok {
		// "Bootup is not yet finished": a job is still running.
		result.Verdict = Unavailable
		result.Summary = env.t("diagnostics.summary.boot.unfinished")
		result.Metrics["reason"] = "boot-unfinished"
		result.DurationSeconds = int(time.Since(start).Seconds())
		return result
	}
	result.Metrics["phases"] = times.phases
	result.Metrics["totalSeconds"] = times.total
	if times.graphical > 0 {
		result.Metrics["graphicalSeconds"] = times.graphical
	}

	progress(35, env.t("diagnostics.progress.boot.blame"))
	out, _, err = env.Run(ctx, "systemd-analyze", "blame", "--no-pager")
	if err != nil {
		return failedToRun(env, result, "systemd-analyze", err, start)
	}
	blame := parseBlame(string(out))
	graphicalMono, _ := strconv.ParseUint(showValue(ctx, env, "ActiveEnterTimestampMonotonic", "graphical.target"), 10, 64)
	var slowest []unitTime
	for _, unit := range blame {
		if strings.HasSuffix(unit.Unit, ".device") || unit.Seconds < 0.5 || len(slowest) == 5 {
			continue
		}
		if startedBeforeLogin(ctx, env, unit.Unit, graphicalMono) {
			slowest = append(slowest, unit)
		}
	}
	result.Metrics["slowest"] = slowest

	progress(55, env.t("diagnostics.progress.boot.chain"))
	out, _, err = env.Run(ctx, "systemd-analyze", "critical-chain", "graphical.target", "--no-pager")
	if err != nil {
		return failedToRun(env, result, "systemd-analyze", err, start)
	}
	chain := parseCriticalChain(string(out))

	if userspace := times.phases["userspace"]; userspace > slowBootSeconds {
		findings = append(findings, env.finding(SeverityAttention, "boot.slow", strconv.FormatFloat(userspace, 'f', 1, 64)+"s",
			"seconds", strconv.Itoa(int(userspace))))
	}

	if seconds := chain[waitOnlineUnit]; seconds > slowWaitOnline {
		finding := env.finding(SeverityAttention, "boot.wait-online", strconv.FormatFloat(seconds, 'f', 1, 64)+"s",
			"seconds", strconv.Itoa(int(seconds)))
		if wanted := networkOnlineWanted(ctx, env); len(wanted) == 0 {
			finding.Remedy = "disable-wait-online"
		} else {
			finding.Raw += "\n" + strings.Join(wanted, " ")
		}
		findings = append(findings, finding)
	}

	progress(75, env.t("diagnostics.progress.boot.devices"))
	out, _, err = env.Run(ctx, "journalctl", "-b", "-o", "cat", "--no-pager", "-q", "-g", "Timed out waiting for device")
	if err == nil {
		var devices []string
		for _, line := range strings.Split(string(out), "\n") {
			if device, ok := strings.CutPrefix(strings.TrimSpace(line), "Timed out waiting for device "); ok {
				devices = append(devices, strings.TrimSuffix(device, "."))
			}
		}
		sort.Strings(devices)
		devices = dedupe(devices)
		if len(devices) > 0 {
			findings = append(findings, env.finding(SeverityAttention, "boot.missing-device", strings.Join(devices, "\n"),
				"count", strconv.Itoa(len(devices))))
		}
	}

	progress(90, env.t("diagnostics.progress.boot.services"))
	for _, unit := range slowest {
		if unit.Seconds <= slowServiceSeconds || unit.Unit == waitOnlineUnit || !strings.HasSuffix(unit.Unit, ".service") {
			continue
		}
		seconds := strconv.Itoa(int(unit.Seconds))
		finding := env.finding(SeverityAttention, "boot.slow-service", unit.Unit+" "+seconds+"s", "unit", unit.Unit, "seconds", seconds)
		if validUnitName(unit.Unit) && userEnabled(ctx, env, unit.Unit) {
			finding.Remedy = "disable-service"
			finding.RemedyArgs = []string{unit.Unit}
		}
		findings = append(findings, finding)
	}

	result.Findings = findings
	result.Verdict = verdictFromFindings(findings)
	shown := times.total
	if times.graphical > 0 {
		shown = times.total - times.phases["userspace"] + times.graphical
	}
	result.Summary = env.t("diagnostics.summary.boot", "seconds", strconv.FormatFloat(shown, 'f', 1, 64))
	result.DurationSeconds = int(time.Since(start).Seconds())
	return result
}

func dedupe(sorted []string) []string {
	out := sorted[:0]
	for i, value := range sorted {
		if i == 0 || value != sorted[i-1] {
			out = append(out, value)
		}
	}
	return out
}

// disableWaitOnline checks again that nothing enabled needs the network at
// boot (the report may be old) before disabling the wait.
func disableWaitOnline(ctx context.Context, env *Env, _ []string, progress Progress) StageResult {
	start := time.Now()
	result := remedyStage("disable-wait-online")
	progress(10, env.t("diagnostics.progress.repair.wait-online"))
	if wanted := networkOnlineWanted(ctx, env); len(wanted) > 0 {
		result.Verdict = Unavailable
		result.Summary = env.t("diagnostics.summary.repair.wait-online-needed", "units", strings.Join(wanted, ", "))
		result.DurationSeconds = int(time.Since(start).Seconds())
		return result
	}
	out, code, err := env.Run(ctx, "systemctl", "disable", waitOnlineUnit)
	result = finishRemedy(env, result, "systemctl", out, code, err, start)
	if result.Verdict == Ok {
		result.Summary = env.t("diagnostics.summary.repair.wait-online")
	}
	return result
}

// disableService disables only units the image does not enable by preset,
// checked again here; the image's services are never touched.
func disableService(ctx context.Context, env *Env, args []string, progress Progress) StageResult {
	start := time.Now()
	result := remedyStage("disable-service")
	var done []string
	for _, unit := range args {
		if !validUnitName(unit) || !strings.HasSuffix(unit, ".service") || !userEnabled(ctx, env, unit) {
			continue
		}
		progress(uint32(10+80*len(done)/max(1, len(args))), env.t("diagnostics.progress.repair.disable", "unit", unit))
		out, code, err := env.Run(ctx, "systemctl", "disable", unit)
		if result = finishRemedy(env, result, "systemctl", out, code, err, start); result.Verdict != Ok {
			return result
		}
		done = append(done, unit)
	}
	result.DurationSeconds = int(time.Since(start).Seconds())
	if len(done) == 0 {
		result.Verdict = Unavailable
		result.Summary = env.t("diagnostics.summary.unavailable")
		return result
	}
	result.Verdict = Ok
	result.Metrics["disabled"] = done
	result.Summary = env.t("diagnostics.summary.repair.disable", "units", strings.Join(done, ", "))
	return result
}
