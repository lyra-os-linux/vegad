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
	// NoSnapshot: the fix touches no file a snapshot could restore (network
	// settings live in NetworkManager's runtime state).
	NoSnapshot bool
}

// Remedies are the contract's remedy ids, in the order Repair applies them:
// packages first, then services, then the clock.
var Remedies = []Remedy{
	{ID: "reinstall-packages", Retest: "packages", Run: reinstallPackages},
	{ID: "verify-dependencies", Retest: "packages", Run: verifyDependencies},
	{ID: "restart-units", Retest: "logs", Run: restartUnits},
	{ID: "enable-ntp", Retest: "platform", Run: enableNTP},
	{ID: "enable-firewall", Retest: "security", Run: enableFirewall},
	{ID: "close-port", Retest: "security", Run: closePorts},
	{ID: "apply-security-patches", Retest: "security", Run: applySecurityPatches},
	{ID: "disable-wait-online", Retest: "boot", Run: disableWaitOnline},
	{ID: "disable-service", Retest: "boot", Run: disableService},
	{ID: "reconnect", Retest: "network", Run: reconnect, NoSnapshot: true},
	{ID: "renew-dhcp", Retest: "network", Run: renewDHCP, NoSnapshot: true},
	{ID: "temporary-dns", Retest: "network", Run: temporaryDNSFix, NoSnapshot: true},
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
	zonePattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	servicePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	portSpecPattern    = regexp.MustCompile(`^[0-9]{1,5}(-[0-9]{1,5})?/(tcp|udp|sctp|dccp)$`)
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

func enableFirewall(ctx context.Context, env *Env, _ []string, progress Progress) StageResult {
	start := time.Now()
	result := remedyStage("enable-firewall")
	progress(10, env.t("diagnostics.progress.repair.firewall"))
	out, code, err := env.Run(ctx, "systemctl", "enable", "--now", "firewalld")
	result = finishRemedy(env, result, "systemctl", out, code, err, start)
	if result.Verdict == Ok {
		result.Summary = env.t("diagnostics.summary.repair.firewall")
	}
	return result
}

// closeTarget is one "zone|service:name" or "zone|port:8000/tcp" argument.
type closeTarget struct {
	zone, kind, value string
}

func parseCloseTarget(arg string) (closeTarget, bool) {
	zone, allow, ok := strings.Cut(arg, "|")
	if !ok || !zonePattern.MatchString(zone) {
		return closeTarget{}, false
	}
	kind, value, ok := strings.Cut(allow, ":")
	switch {
	case !ok:
		return closeTarget{}, false
	case kind == "service" && servicePattern.MatchString(value):
	case kind == "port" && portSpecPattern.MatchString(value):
	default:
		return closeTarget{}, false
	}
	return closeTarget{zone, kind, value}, true
}

// closePorts removes services or ports from their zones, permanently and in
// the running configuration. Each argument is validated again here: the
// stored report is trusted only as far as the patterns.
func closePorts(ctx context.Context, env *Env, args []string, progress Progress) StageResult {
	start := time.Now()
	result := remedyStage("close-port")
	var closed []string
	for i, arg := range args {
		target, ok := parseCloseTarget(arg)
		if !ok {
			continue
		}
		progress(uint32(10+80*i/max(1, len(args))), env.t("diagnostics.progress.repair.close", "item", target.value, "zone", target.zone))
		flag := "--remove-" + target.kind + "=" + target.value
		for _, extra := range [][]string{{"--permanent"}, nil} {
			cmd := append([]string{"--zone=" + target.zone, flag}, extra...)
			out, code, err := env.Run(ctx, "firewall-cmd", cmd...)
			if err != nil {
				return failedToRun(env, result, "firewall-cmd", err, start)
			}
			if code != 0 {
				result.Findings = append(result.Findings, env.finding(SeverityFail, "repair.failed", lastLines(string(out), 12), "tool", "firewall-cmd", "code", strconv.Itoa(code)))
			}
		}
		closed = append(closed, target.zone+": "+target.value)
	}
	if len(closed) == 0 {
		result.Verdict = Unavailable
		result.Summary = env.t("diagnostics.summary.unavailable")
		return result
	}
	result.Metrics["closed"] = closed
	result.DurationSeconds = int(time.Since(start).Seconds())
	result.Verdict = verdictFromFindings(result.Findings)
	result.Summary = env.t("diagnostics.summary.repair.close", "items", strings.Join(closed, ", "))
	return result
}

func applySecurityPatches(ctx context.Context, env *Env, _ []string, progress Progress) StageResult {
	start := time.Now()
	result := remedyStage("apply-security-patches")
	progress(10, env.t("diagnostics.progress.repair.patches"))
	out, code, err := env.Run(ctx, "zypper", "-n", "patch", "--category", "security", "--auto-agree-with-licenses")
	// zypper patch exits 102 or 103 when a restart is needed: applied, with
	// a restart left to the user.
	if err == nil && (code == 102 || code == 103) {
		code = 0
		result.Findings = append(result.Findings, env.finding(Info, "repair.restart-needed", lastLines(string(out), 6)))
	}
	result = finishRemedy(env, result, "zypper", out, code, err, start)
	if result.Verdict == Ok {
		result.Summary = env.t("diagnostics.summary.repair.patches")
	}
	return result
}
