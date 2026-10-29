package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type lsblkOutput struct {
	Blockdevices []struct {
		Name string  `json:"name"`
		Type string  `json:"type"`
		Tran *string `json:"tran"`
	} `json:"blockdevices"`
}

// physicalDisks keeps whole disks with a transport (nvme, sata, usb...),
// which drops zram, loop and device-mapper devices.
func physicalDisks(out []byte) ([]string, error) {
	var parsed lsblkOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, err
	}
	var disks []string
	for _, dev := range parsed.Blockdevices {
		if dev.Type == "disk" && dev.Tran != nil && *dev.Tran != "" {
			disks = append(disks, dev.Name)
		}
	}
	return disks, nil
}

type smartReport struct {
	ModelName    string `json:"model_name"`
	SerialNumber string `json:"serial_number"`
	Device       struct {
		Protocol string `json:"protocol"`
	} `json:"device"`
	SmartStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature struct {
		Current       int `json:"current"`
		OpLimitMax    int `json:"op_limit_max"`
		CriticalLimit int `json:"critical_limit_max"`
	} `json:"temperature"`
	PowerOnTime struct {
		Hours int64 `json:"hours"`
	} `json:"power_on_time"`
	NVMe *struct {
		CriticalWarning         int   `json:"critical_warning"`
		AvailableSpare          int   `json:"available_spare"`
		AvailableSpareThreshold int   `json:"available_spare_threshold"`
		PercentageUsed          int   `json:"percentage_used"`
		DataUnitsWritten        int64 `json:"data_units_written"`
		PowerOnHours            int64 `json:"power_on_hours"`
		MediaErrors             int64 `json:"media_errors"`
		NumErrLogEntries        int64 `json:"num_err_log_entries"`
		UnsafeShutdowns         int64 `json:"unsafe_shutdowns"`
	} `json:"nvme_smart_health_information_log"`
	ATA *struct {
		Table []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Raw  struct {
				Value int64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
}

// smartctl exit status bits (smartctl(8)).
const (
	smartBitCommandLine = 1 << 0
	smartBitOpenFailed  = 1 << 1
	smartBitDiskFailing = 1 << 3
	smartBitPrefail     = 1 << 4
	smartBitPastPrefail = 1 << 5
	smartBitErrorLog    = 1 << 6
	smartBitSelfTestLog = 1 << 7
)

// pohUnreliable flags NVMe controllers (often DRAM-less) whose power-on
// counter is too low for the volume written: sustaining that volume would
// need more than ~50 MB/s for every hour the drive claims to have been on.
func pohUnreliable(hours, dataUnitsWritten int64) bool {
	written := float64(dataUnitsWritten) * 512000
	return written > float64(hours)*3600*50e6
}

// analyzeSmart turns one smartctl report into findings and metrics.
func analyzeSmart(env *Env, dev string, out []byte, exit int) ([]Finding, map[string]any, []string) {
	var report smartReport
	if err := json.Unmarshal(out, &report); err != nil || exit&(smartBitCommandLine|smartBitOpenFailed) != 0 {
		return []Finding{env.finding(SeverityAttention, "smart.unreadable", strings.TrimSpace(string(out)), "device", dev)}, nil, nil
	}
	var findings []Finding
	metrics := map[string]any{"model": report.ModelName, "tempC": report.Temperature.Current}
	var sensitive []string
	if report.SerialNumber != "" {
		sensitive = append(sensitive, report.SerialNumber)
	}
	if report.SmartStatus != nil {
		metrics["smartPassed"] = report.SmartStatus.Passed
		if !report.SmartStatus.Passed {
			findings = append(findings, env.finding(SeverityFail, "smart.failed", "", "device", dev))
		}
	}
	if exit&smartBitDiskFailing != 0 && (report.SmartStatus == nil || report.SmartStatus.Passed) {
		findings = append(findings, env.finding(SeverityFail, "smart.failed", "exit "+strconv.Itoa(exit), "device", dev))
	}
	if exit&(smartBitPrefail|smartBitPastPrefail) != 0 {
		findings = append(findings, env.finding(SeverityAttention, "smart.prefail", "exit "+strconv.Itoa(exit), "device", dev))
	}
	if n := report.NVMe; n != nil {
		metrics["percentUsed"] = n.PercentageUsed
		metrics["availableSpare"] = n.AvailableSpare
		metrics["mediaErrors"] = n.MediaErrors
		metrics["errorLogEntries"] = n.NumErrLogEntries
		metrics["powerOnHours"] = n.PowerOnHours
		metrics["dataWrittenTB"] = float64(n.DataUnitsWritten) * 512000 / 1e12
		if n.CriticalWarning != 0 {
			findings = append(findings, env.finding(SeverityFail, "smart.critical-warning", fmt.Sprintf("critical_warning=0x%02x", n.CriticalWarning), "device", dev))
		}
		if n.AvailableSpareThreshold > 0 && n.AvailableSpare < n.AvailableSpareThreshold {
			findings = append(findings, env.finding(SeverityFail, "smart.spare-low", fmt.Sprintf("available_spare=%d threshold=%d", n.AvailableSpare, n.AvailableSpareThreshold), "device", dev))
		}
		if n.PercentageUsed >= 90 {
			findings = append(findings, env.finding(SeverityAttention, "smart.worn", fmt.Sprintf("percentage_used=%d", n.PercentageUsed), "device", dev, "percent", strconv.Itoa(n.PercentageUsed)))
		}
		if n.MediaErrors > 0 {
			findings = append(findings, env.finding(SeverityAttention, "smart.media-errors", fmt.Sprintf("media_errors=%d", n.MediaErrors), "device", dev, "count", strconv.FormatInt(n.MediaErrors, 10)))
		}
		if pohUnreliable(n.PowerOnHours, n.DataUnitsWritten) {
			raw := fmt.Sprintf("power_on_hours=%d data_units_written=%d (%.1f TB)", n.PowerOnHours, n.DataUnitsWritten, metrics["dataWrittenTB"])
			findings = append(findings, env.finding(Info, "smart.poh-unreliable", raw, "device", dev))
		}
	} else if exit&smartBitErrorLog != 0 {
		// The NVMe error log counts benign host commands; only ATA error
		// log entries point at the drive.
		findings = append(findings, env.finding(SeverityAttention, "smart.error-log", "exit "+strconv.Itoa(exit), "device", dev))
	}
	if exit&smartBitSelfTestLog != 0 {
		findings = append(findings, env.finding(SeverityAttention, "smart.self-test-errors", "exit "+strconv.Itoa(exit), "device", dev))
	}
	if a := report.ATA; a != nil {
		for _, attr := range a.Table {
			switch attr.ID {
			case 5, 197, 198:
				metrics[attr.Name] = attr.Raw.Value
				if attr.Raw.Value > 0 {
					findings = append(findings, env.finding(SeverityAttention, "smart.sectors", fmt.Sprintf("%d %s=%d", attr.ID, attr.Name, attr.Raw.Value), "device", dev, "attribute", attr.Name, "count", strconv.FormatInt(attr.Raw.Value, 10)))
				}
			}
		}
	}
	if t := report.Temperature; t.OpLimitMax > 0 && t.Current >= t.OpLimitMax {
		findings = append(findings, env.finding(SeverityAttention, "smart.hot", fmt.Sprintf("current=%d op_limit_max=%d", t.Current, t.OpLimitMax), "device", dev, "temp", strconv.Itoa(t.Current)))
	}
	return findings, metrics, sensitive
}

// btrfsDeviceErrors parses `btrfs device stats -c` lines such as
// "[/dev/nvme0n1p2].write_io_errs    0".
func btrfsDeviceErrors(out string) (map[string]int64, int64) {
	counters := map[string]int64{}
	var total int64
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		dot := strings.LastIndex(fields[0], "].")
		if dot < 0 {
			continue
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		counters[fields[0][dot+2:]] += value
		total += value
	}
	return counters, total
}

func runDiskHealth(ctx context.Context, env *Env, progress Progress) StageResult {
	start := time.Now()
	result := StageResult{ID: "disk-health", Metrics: map[string]any{}}
	progress(5, env.t("diagnostics.progress.disk-health.list"))
	out, _, err := env.Run(ctx, "lsblk", "-J", "-d", "-o", "NAME,TYPE,TRAN")
	if err != nil {
		return failedToRun(env, result, "lsblk", err, start)
	}
	disks, err := physicalDisks(out)
	if err != nil {
		return failedToRun(env, result, "lsblk", err, start)
	}
	var passed, total int
	for i, disk := range disks {
		progress(uint32(10+80*i/len(disks)), env.t("diagnostics.progress.disk-health.smart", "device", disk))
		out, code, err := env.Run(ctx, "smartctl", "-a", "-j", "/dev/"+disk)
		if err != nil {
			return failedToRun(env, result, "smartctl", err, start)
		}
		findings, metrics, sensitive := analyzeSmart(env, disk, out, code)
		result.Findings = append(result.Findings, findings...)
		result.Sensitive = append(result.Sensitive, sensitive...)
		if metrics != nil {
			result.Metrics[disk] = metrics
			total++
			if verdictFromFindings(findings) != Fail {
				passed++
			}
		}
	}
	if rootIsBtrfs(env) {
		if _, err := env.LookPath("btrfs"); err == nil {
			progress(92, env.t("diagnostics.progress.disk-health.btrfs"))
			out, code, err := env.Run(ctx, "btrfs", "device", "stats", "-c", "/")
			if err != nil {
				return failedToRun(env, result, "btrfs", err, start)
			}
			counters, errorsTotal := btrfsDeviceErrors(string(out))
			result.Metrics["btrfsErrors"] = counters
			if errorsTotal > 0 || code != 0 {
				severity := SeverityAttention
				if counters["corruption_errs"] > 0 || counters["generation_errs"] > 0 {
					severity = SeverityFail
				}
				result.Findings = append(result.Findings, env.finding(severity, "btrfs.device-errors", strings.TrimSpace(string(out)), "count", strconv.FormatInt(errorsTotal, 10)))
			}
		}
	}
	result.Verdict = verdictFromFindings(result.Findings)
	if total == 0 {
		result.Findings = append(result.Findings, env.finding(Info, "smart.no-disks", ""))
	}
	result.Summary = env.t("diagnostics.summary.disk-health", "passed", strconv.Itoa(passed), "total", strconv.Itoa(total))
	result.DurationSeconds = int(time.Since(start).Seconds())
	return result
}
