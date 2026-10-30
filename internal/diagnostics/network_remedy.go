package diagnostics

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// interfacePattern is a kernel interface name (IFNAMSIZ 16 with the NUL).
var interfacePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,14}$`)

// The DNS servers of temporary-dns: Cloudflare and Quad9.
const temporaryDNS = "1.1.1.1 9.9.9.9"

// networkDevice returns the one valid interface argument of a remedy.
func networkDevice(args []string) (string, bool) {
	for _, arg := range args {
		if interfacePattern.MatchString(arg) {
			return arg, true
		}
	}
	return "", false
}

func noDevice(env *Env, result StageResult, start time.Time) StageResult {
	result.Verdict = Unavailable
	result.Summary = env.t("diagnostics.summary.unavailable")
	result.DurationSeconds = int(time.Since(start).Seconds())
	return result
}

// reconnect turns Wi-Fi on when it is off and asks NetworkManager to
// connect the device with its usual profile.
func reconnect(ctx context.Context, env *Env, args []string, progress Progress) StageResult {
	start := time.Now()
	result := remedyStage("reconnect")
	device, ok := networkDevice(args)
	if !ok {
		return noDevice(env, result, start)
	}
	progress(10, env.t("diagnostics.progress.repair.reconnect", "device", device))
	if out, _, err := env.Run(ctx, "nmcli", "radio", "wifi"); err == nil && strings.TrimSpace(string(out)) == "disabled" {
		out, code, err := env.Run(ctx, "nmcli", "radio", "wifi", "on")
		if result = finishRemedy(env, result, "nmcli", out, code, err, start); result.Verdict != Ok {
			return result
		}
		// NetworkManager needs a moment to bring the radio up.
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
	out, code, err := env.Run(ctx, "nmcli", "--wait", "30", "device", "connect", device)
	result = finishRemedy(env, result, "nmcli", out, code, err, start)
	if result.Verdict == Ok {
		result.Summary = env.t("diagnostics.summary.repair.reconnect", "device", device)
	}
	return result
}

// renewDHCP reapplies the active connection, which restarts DHCP; if the
// device refuses, it activates the connection again.
func renewDHCP(ctx context.Context, env *Env, args []string, progress Progress) StageResult {
	start := time.Now()
	result := remedyStage("renew-dhcp")
	device, ok := networkDevice(args)
	if !ok {
		return noDevice(env, result, start)
	}
	progress(10, env.t("diagnostics.progress.repair.dhcp", "device", device))
	out, code, err := env.Run(ctx, "nmcli", "--wait", "30", "device", "reapply", device)
	if err == nil && code != 0 {
		out, code, err = env.Run(ctx, "nmcli", "--wait", "30", "device", "connect", device)
	}
	result = finishRemedy(env, result, "nmcli", out, code, err, start)
	if result.Verdict == Ok {
		result.Summary = env.t("diagnostics.summary.repair.dhcp", "device", device)
	}
	return result
}

// temporaryDNSFix uses public DNS servers in the running activation of the
// device only ("device modify"): the saved profile does not change, and the
// next connection is back to the usual servers.
func temporaryDNSFix(ctx context.Context, env *Env, args []string, progress Progress) StageResult {
	start := time.Now()
	result := remedyStage("temporary-dns")
	device, ok := networkDevice(args)
	if !ok {
		return noDevice(env, result, start)
	}
	progress(10, env.t("diagnostics.progress.repair.dns", "device", device))
	out, code, err := env.Run(ctx, "nmcli", "device", "modify", device,
		"ipv4.ignore-auto-dns", "yes", "ipv4.dns", temporaryDNS, "ipv6.ignore-auto-dns", "yes")
	result = finishRemedy(env, result, "nmcli", out, code, err, start)
	if result.Verdict == Ok {
		result.Summary = env.t("diagnostics.summary.repair.dns", "device", device)
	}
	return result
}
