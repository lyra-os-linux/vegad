package diagnostics

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Remedy is a safe fix Repair can apply to findings of a stored report.
// Nothing here touches disks, partitions or file systems.
type Remedy struct {
	ID string
	// Retest is the test that runs again to confirm the fix.
	Retest string
	Run    func(ctx context.Context, env *Env, args []string, progress Progress) StageResult
}

// Remedies are the contract's remedy ids, in the order Repair applies them:
// packages first, then services, then the clock.
var Remedies = []Remedy{
	{ID: "reinstall-packages", Retest: "packages", Run: reinstallPackages},
	{ID: "verify-dependencies", Retest: "packages", Run: verifyDependencies},
	{ID: "restart-units", Retest: "logs", Run: restartUnits},
	{ID: "enable-ntp", Retest: "platform", Run: enableNTP},
}

// LookupRemedy returns the remedy with id.
func LookupRemedy(id string) (Remedy, bool) {
	for _, remedy := range Remedies {
		if remedy.ID == id {
			return remedy, true
		}
	}
	return Remedy{}, false
}

var (
	packageNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._-]*$`)
	unitNamePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._:\\-]*\.(service|socket|timer|mount|path|target)$`)
)

// The stored report is trusted only as far as these patterns: even a
// tampered report cannot turn an argument into an option or a path.
func validPackageName(name string) bool { return packageNamePattern.MatchString(name) }
func validUnitName(name string) bool    { return unitNamePattern.MatchString(name) }

func remedyStage(remedy string) StageResult {
	return StageResult{ID: "repair:" + remedy, Findings: []Finding{}, Metrics: map[string]any{}}
}

// finishRemedy turns a tool outcome into the repair stage verdict: ok when
// the fix was applied, fail when it was not.
func finishRemedy(env *Env, result StageResult, tool string, out []byte, code int, err error, start time.Time) StageResult {
	result.DurationSeconds = int(time.Since(start).Seconds())
	if err != nil {
		return failedToRun(env, result, tool, err, start)
	}
	if code != 0 {
		result.Verdict = Fail
		result.Summary = env.t("diagnostics.summary.repair-failed")
		result.Findings = append(result.Findings, env.finding(SeverityFail, "repair.failed", lastLines(string(out), 20), "tool", tool, "code", strconv.Itoa(code)))
		return result
	}
	result.Verdict = Ok
	return result
}

func reinstallPackages(ctx context.Context, env *Env, args []string, progress Progress) StageResult {
	start := time.Now()
	result := remedyStage("reinstall-packages")
	var packages []string
	for _, name := range args {
		if validPackageName(name) {
			packages = append(packages, name)
		}
	}
	if len(packages) == 0 {
		result.Verdict = Unavailable
		result.Summary = env.t("diagnostics.summary.unavailable")
		return result
	}
	result.Metrics["packages"] = packages
	progress(10, env.t("diagnostics.progress.repair.reinstall", "packages", strings.Join(packages, ", ")))
	out, code, err := env.Run(ctx, "zypper", append([]string{"-n", "--no-refresh", "install", "-f"}, packages...)...)
	result = finishRemedy(env, result, "zypper", out, code, err, start)
	if result.Verdict == Ok {
		result.Summary = env.t("diagnostics.summary.repair.reinstall", "packages", strings.Join(packages, ", "))
	}
	return result
}

func verifyDependencies(ctx context.Context, env *Env, _ []string, progress Progress) StageResult {
	start := time.Now()
	result := remedyStage("verify-dependencies")
	progress(10, env.t("diagnostics.progress.packages.zypper"))
	out, code, err := env.Run(ctx, "zypper", "-n", "verify")
	result = finishRemedy(env, result, "zypper", out, code, err, start)
	if result.Verdict == Ok {
		result.Summary = env.t("diagnostics.summary.repair.verify")
	}
	return result
}

func restartUnits(ctx context.Context, env *Env, args []string, progress Progress) StageResult {
	start := time.Now()
	result := remedyStage("restart-units")
	var restarted, failed []string
	for i, unit := range args {
		if !validUnitName(unit) {
			continue
		}
		progress(uint32(10+80*i/max(1, len(args))), env.t("diagnostics.progress.repair.restart", "unit", unit))
		_, _, _ = env.Run(ctx, "systemctl", "reset-failed", "--", unit)
		out, code, err := env.Run(ctx, "systemctl", "restart", "--", unit)
		if err != nil {
			return failedToRun(env, result, "systemctl", err, start)
		}
		state, _, _ := env.Run(ctx, "systemctl", "is-failed", "--", unit)
		if code != 0 || strings.TrimSpace(string(state)) == "failed" {
			failed = append(failed, unit)
			result.Findings = append(result.Findings, env.finding(SeverityFail, "repair.unit-failed", lastLines(string(out), 12), "unit", unit))
			continue
		}
		restarted = append(restarted, unit)
	}
	result.Metrics["restarted"] = restarted
	result.Metrics["stillFailed"] = failed
	result.DurationSeconds = int(time.Since(start).Seconds())
	result.Verdict = verdictFromFindings(result.Findings)
	result.Summary = env.t("diagnostics.summary.repair.restart", "ok", strconv.Itoa(len(restarted)), "total", strconv.Itoa(len(restarted)+len(failed)))
	return result
}

func enableNTP(ctx context.Context, env *Env, _ []string, progress Progress) StageResult {
	start := time.Now()
	result := remedyStage("enable-ntp")
	progress(10, env.t("diagnostics.progress.repair.ntp"))
	out, code, err := env.Run(ctx, "timedatectl", "set-ntp", "true")
	result = finishRemedy(env, result, "timedatectl", out, code, err, start)
	if result.Verdict == Ok {
		result.Summary = env.t("diagnostics.summary.repair.ntp")
	}
	return result
}
