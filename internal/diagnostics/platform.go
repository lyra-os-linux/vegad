package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// hwmonTemps reads every temperature sensor in °C, keyed "<chip>/<label>".
func hwmonTemps(env *Env) map[string]int {
	temps := map[string]int{}
	inputs, _ := env.Glob("/sys/class/hwmon/hwmon*/temp*_input")
	for _, input := range inputs {
		dir := filepath.Dir(input)
		chip := readTrimmed(env, filepath.Join(dir, "name"))
		value, err := strconv.Atoi(readTrimmed(env, input))
		if err != nil || chip == "" {
			continue
		}
		base := strings.TrimSuffix(filepath.Base(input), "_input")
		label := readTrimmed(env, filepath.Join(dir, base+"_label"))
		if label == "" {
			label = base
		}
		temps[chip+"/"+label] = value / 1000
	}
	return temps
}

func readTrimmed(env *Env, path string) string {
	data, err := env.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

type battery struct {
	Capacity float64
	Cycles   int
	Present  bool
}

// parseUpowerBattery reads `upower -i <battery>`.
func parseUpowerBattery(out string) battery {
	var b battery
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "present":
			b.Present = value == "yes"
		case "capacity":
			b.Capacity, _ = strconv.ParseFloat(strings.TrimSuffix(value, "%"), 64)
		case "charge-cycles":
			b.Cycles, _ = strconv.Atoi(value)
		}
	}
	return b
}

// taintFlags are the kernel taint letters by bit (Documentation/admin-guide/tainted-kernels.rst).
const taintFlags = "PFSRMBUDAWCIOELKXTN"

// worrying taints point at hardware or kernel faults; the rest (proprietary
// or out-of-tree modules such as NVIDIA, unsigned modules) are expected.
const worryingTaints = "MBDWL"

func decodeTaint(value uint64) string {
	var flags strings.Builder
	for bit := 0; bit < len(taintFlags); bit++ {
		if value&(1<<bit) != 0 {
			flags.WriteByte(taintFlags[bit])
		}
	}
	return flags.String()
}

func parseKeyValues(out string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return values
}

type fwupdUpdates struct {
	Devices []struct {
		Name     string `json:"Name"`
		Releases []struct {
			Version string `json:"Version"`
		} `json:"Releases"`
	} `json:"Devices"`
}

func runPlatform(ctx context.Context, env *Env, progress Progress) StageResult {
	start := time.Now()
	result := StageResult{ID: "platform", Metrics: map[string]any{}}
	var findings []Finding
	have := func(tool string) bool { _, err := env.LookPath(tool); return err == nil }

	progress(5, env.t("diagnostics.progress.platform.sensors"))
	temps := hwmonTemps(env)
	result.Metrics["idleTempsC"] = temps
	hottest, hottestName := 0, ""
	for name, value := range temps {
		if strings.HasPrefix(name, "coretemp/") || strings.HasPrefix(name, "k10temp/") {
			if value > hottest {
				hottest, hottestName = value, name
			}
		}
	}
	if hottest >= 70 {
		findings = append(findings, env.finding(SeverityAttention, "platform.hot-idle", fmt.Sprintf("%s=%d", hottestName, hottest), "temp", strconv.Itoa(hottest)))
	}

	if have("upower") {
		progress(20, env.t("diagnostics.progress.platform.battery"))
		out, _, err := env.Run(ctx, "upower", "-e")
		if err != nil {
			return failedToRun(env, result, "upower", err, start)
		}
		for _, device := range strings.Fields(string(out)) {
			if !strings.Contains(device, "battery_") {
				continue
			}
			info, _, err := env.Run(ctx, "upower", "-i", device)
			if err != nil {
				return failedToRun(env, result, "upower", err, start)
			}
			b := parseUpowerBattery(string(info))
			if !b.Present || b.Capacity == 0 {
				continue
			}
			result.Metrics["batteryCapacityPercent"] = b.Capacity
			result.Metrics["batteryCycles"] = b.Cycles
			if b.Capacity < 60 {
				findings = append(findings, env.finding(SeverityAttention, "platform.battery-worn", fmt.Sprintf("capacity=%.1f%% cycles=%d", b.Capacity, b.Cycles), "percent", strconv.Itoa(int(b.Capacity))))
			}
		}
	}

	if have("fwupdmgr") {
		progress(40, env.t("diagnostics.progress.platform.firmware"))
		fwCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		out, _, err := env.Run(fwCtx, "fwupdmgr", "get-updates", "--json", "--no-unreported-check", "--no-metadata-check")
		cancel()
		if ctx.Err() != nil {
			return failedToRun(env, result, "fwupdmgr", ctx.Err(), start)
		}
		var updates fwupdUpdates
		if err == nil && json.Unmarshal(out, &updates) == nil {
			var names []string
			for _, device := range updates.Devices {
				if len(device.Releases) > 0 {
					names = append(names, device.Name+" → "+device.Releases[0].Version)
				}
			}
			result.Metrics["firmwareUpdates"] = len(names)
			if len(names) > 0 {
				findings = append(findings, env.finding(Info, "platform.firmware-updates", strings.Join(names, "\n"), "count", strconv.Itoa(len(names))))
			}
		}
	}

	progress(70, env.t("diagnostics.progress.platform.state"))
	if have("timedatectl") {
		out, _, err := env.Run(ctx, "timedatectl", "show")
		if err != nil {
			return failedToRun(env, result, "timedatectl", err, start)
		}
		values := parseKeyValues(string(out))
		synced := values["NTPSynchronized"] == "yes"
		result.Metrics["ntpSynchronized"] = synced
		result.Metrics["ntpEnabled"] = values["NTP"] == "yes"
		raw := "NTP=" + values["NTP"] + " NTPSynchronized=" + values["NTPSynchronized"]
		switch {
		case values["NTP"] != "yes":
			// Off: the clock is right today and drifts from now on. Turning
			// synchronization on is the fix.
			off := env.finding(SeverityAttention, "platform.ntp-off", raw)
			off.Remedy = "enable-ntp"
			findings = append(findings, off)
		case !synced:
			// On but unanswered: the network, not a setting, is the cause.
			findings = append(findings, env.finding(SeverityAttention, "platform.ntp", raw))
		}
	}
	if have("mokutil") {
		out, _, err := env.Run(ctx, "mokutil", "--sb-state")
		if err != nil {
			return failedToRun(env, result, "mokutil", err, start)
		}
		state := strings.TrimSpace(string(out))
		enabled := strings.Contains(state, "SecureBoot enabled")
		result.Metrics["secureBoot"] = enabled
		if !enabled {
			findings = append(findings, env.finding(Info, "platform.secure-boot-off", state))
		}
	}
	if raw := readTrimmed(env, "/proc/sys/kernel/tainted"); raw != "" {
		value, _ := strconv.ParseUint(raw, 10, 64)
		flags := decodeTaint(value)
		result.Metrics["kernelTaint"] = flags
		if strings.ContainsAny(flags, worryingTaints) {
			findings = append(findings, env.finding(SeverityAttention, "platform.kernel-tainted", raw+" ("+flags+")", "flags", flags))
		} else if flags != "" {
			findings = append(findings, env.finding(Info, "platform.kernel-tainted-modules", raw+" ("+flags+")", "flags", flags))
		}
	}
	if enforce := readTrimmed(env, "/sys/fs/selinux/enforce"); enforce != "" {
		mode := map[string]string{"1": "enforcing", "0": "permissive"}[enforce]
		result.Metrics["selinux"] = mode
	} else {
		result.Metrics["selinux"] = "disabled"
	}
	if sleep := readTrimmed(env, "/sys/power/mem_sleep"); sleep != "" {
		result.Metrics["memSleep"] = sleep
	}

	result.Findings = findings
	result.Verdict = verdictFromFindings(findings)
	result.Summary = env.t("diagnostics.summary.platform", "count", strconv.Itoa(len(findings)))
	result.DurationSeconds = int(time.Since(start).Seconds())
	return result
}
