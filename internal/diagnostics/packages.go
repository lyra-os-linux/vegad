package diagnostics

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// rpmChange is one line of `rpm -Va`.
type rpmChange struct {
	Flags   string // 9 verify flags, or "missing"
	Type    byte   // c, d, g, l, r, or ' '
	Path    string
	Missing bool
}

func (c rpmChange) contentChanged() bool {
	return strings.ContainsAny(c.Flags, "S5")
}

// parseRPMVerify reads `rpm -Va` in the C locale. Lines marked "(replaced)"
// belong to files owned by another package and are skipped, as are ghost
// files (g), which rpm never tracks content for.
func parseRPMVerify(out string) []rpmChange {
	var changes []rpmChange
	for _, line := range strings.Split(out, "\n") {
		if line == "" || strings.HasSuffix(line, "(replaced)") {
			continue
		}
		var change rpmChange
		switch {
		case strings.HasPrefix(line, "missing"):
			change.Missing = true
			change.Flags = "missing"
			line = strings.TrimPrefix(line, "missing")
		case len(line) >= 11 && line[9] == ' ':
			change.Flags = line[:9]
			line = line[9:]
		default:
			continue
		}
		// "  c /path" or "    /path": attribute marker, then the path.
		rest := strings.TrimLeft(line, " ")
		if len(rest) > 2 && rest[1] == ' ' && strings.IndexByte("cdglr", rest[0]) >= 0 {
			change.Type = rest[0]
			rest = rest[2:]
		} else {
			change.Type = ' '
		}
		change.Path = strings.TrimSpace(rest)
		if change.Path == "" || change.Type == 'g' {
			continue
		}
		changes = append(changes, change)
	}
	return changes
}

// knownRewrites are files Lyra rewrites on purpose after installation.
var knownRewrites = []string{"/etc/os-release", "/var/cache/gio-2.0/"}

func isKnownRewrite(path string) bool {
	for _, known := range knownRewrites {
		if path == known || (strings.HasSuffix(known, "/") && strings.HasPrefix(path, known)) {
			return true
		}
	}
	return false
}

var executableDirs = []string{"/usr/bin/", "/usr/sbin/", "/usr/lib/", "/usr/lib64/", "/usr/libexec/", "/bin/", "/sbin/", "/lib/", "/lib64/"}

func isExecutablePath(path string) bool {
	for _, dir := range executableDirs {
		if strings.HasPrefix(path, dir) {
			return true
		}
	}
	return false
}

// owningPackages maps files to the names of the packages that own them.
func owningPackages(ctx context.Context, env *Env, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out, _, err := env.Run(ctx, "rpm", append([]string{"-qf", "--qf", "%{NAME}\n"}, paths...)...)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	for _, name := range strings.Fields(string(out)) {
		if validPackageName(name) && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

func classifyRPMChanges(env *Env, changes []rpmChange) ([]Finding, map[string]any) {
	var binaries, missing, configs, other []string
	for _, c := range changes {
		switch {
		case isKnownRewrite(c.Path):
			other = append(other, c.Path)
		case c.Type == 'c':
			configs = append(configs, c.Path)
		case c.Missing && c.Type != 'd':
			missing = append(missing, c.Path)
		case c.contentChanged() && isExecutablePath(c.Path):
			binaries = append(binaries, c.Flags+" "+c.Path)
		default:
			other = append(other, c.Flags+" "+c.Path)
		}
	}
	var findings []Finding
	if len(binaries) > 0 {
		findings = append(findings, env.finding(SeverityAttention, "packages.binary-changed", strings.Join(binaries, "\n"), "count", strconv.Itoa(len(binaries))))
	}
	if len(missing) > 0 {
		findings = append(findings, env.finding(SeverityAttention, "packages.missing", strings.Join(missing, "\n"), "count", strconv.Itoa(len(missing))))
	}
	if len(configs) > 0 {
		findings = append(findings, env.finding(Info, "packages.config-changed", strings.Join(configs, "\n"), "count", strconv.Itoa(len(configs))))
	}
	if len(other) > 0 {
		findings = append(findings, env.finding(Info, "packages.metadata-changed", strings.Join(other, "\n"), "count", strconv.Itoa(len(other))))
	}
	metrics := map[string]any{
		"binariesChanged": len(binaries), "missingFiles": len(missing),
		"configsChanged": len(configs), "otherChanges": len(other),
	}
	return findings, metrics
}

// zypperDependenciesOK reads `zypper -n verify --dry-run` in the C locale.
func zypperDependenciesOK(out string) bool {
	return strings.Contains(out, "Dependencies of all installed packages are satisfied")
}

func runPackages(ctx context.Context, env *Env, progress Progress) StageResult {
	start := time.Now()
	result := StageResult{ID: "packages"}
	progress(5, env.t("diagnostics.progress.packages.rpm"))
	out, _, err := env.Run(ctx, "rpm", "-Va")
	if err != nil {
		return failedToRun(env, result, "rpm", err, start)
	}
	changes := parseRPMVerify(string(out))
	findings, metrics := classifyRPMChanges(env, changes)
	// Changed binaries and missing files are fixed by reinstalling the
	// packages that own them.
	var broken []string
	for _, c := range changes {
		if !isKnownRewrite(c.Path) && c.Type != 'c' && ((c.Missing && c.Type != 'd') || (c.contentChanged() && isExecutablePath(c.Path))) {
			broken = append(broken, c.Path)
		}
	}
	if packages := owningPackages(ctx, env, broken); len(packages) > 0 {
		for i := range findings {
			if findings[i].Code == "packages.binary-changed" || findings[i].Code == "packages.missing" {
				findings[i].Remedy, findings[i].RemedyArgs = "reinstall-packages", packages
			}
		}
		metrics["packagesToReinstall"] = packages
	}
	progress(80, env.t("diagnostics.progress.packages.zypper"))
	if _, err := env.LookPath("zypper"); err == nil {
		out, code, err := env.Run(ctx, "zypper", "-n", "verify", "--dry-run")
		if err != nil {
			if ctx.Err() != nil {
				return failedToRun(env, result, "zypper", err, start)
			}
		} else if code != 0 || !zypperDependenciesOK(string(out)) {
			broken := env.finding(SeverityAttention, "packages.broken-deps", strings.TrimSpace(string(out)))
			broken.Remedy = "verify-dependencies"
			findings = append(findings, broken)
			metrics["dependenciesSatisfied"] = false
		} else {
			metrics["dependenciesSatisfied"] = true
		}
	}
	result.Findings = findings
	result.Metrics = metrics
	result.Verdict = verdictFromFindings(findings)
	result.Summary = env.t("diagnostics.summary.packages",
		"binaries", fmt.Sprint(metrics["binariesChanged"]),
		"configs", fmt.Sprint(metrics["configsChanged"]))
	result.DurationSeconds = int(time.Since(start).Seconds())
	return result
}

// failedToRun ends a stage whose tool could not run or was cancelled.
func failedToRun(env *Env, result StageResult, tool string, err error, start time.Time) StageResult {
	result.DurationSeconds = int(time.Since(start).Seconds())
	if err == context.Canceled {
		result.Verdict = Cancelled
		result.Summary = env.t("diagnostics.summary.cancelled")
		return result
	}
	if err == context.DeadlineExceeded {
		result.Verdict = Attention
		result.Summary = env.t("diagnostics.summary.timeout")
		result.Findings = append(result.Findings, env.finding(SeverityAttention, "stage.timeout", tool))
		return result
	}
	result.Verdict = Unavailable
	result.Summary = env.t("diagnostics.summary.tool-failed", "tool", tool)
	result.Findings = append(result.Findings, env.finding(Info, "stage.tool-failed", err.Error(), "tool", tool))
	return result
}
