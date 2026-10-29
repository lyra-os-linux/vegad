package diagnostics

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// onLinePower is true when a mains supply is online, or when the machine has
// no mains supply at all (a desktop or a VM without ACPI power devices).
func onLinePower(env *Env) bool {
	types, _ := env.Glob("/sys/class/power_supply/*/type")
	sawMains := false
	for _, typeFile := range types {
		if readTrimmed(env, typeFile) != "Mains" {
			continue
		}
		sawMains = true
		if readTrimmed(env, filepath.Join(filepath.Dir(typeFile), "online")) == "1" {
			return true
		}
	}
	return !sawMains
}

// memAvailable reads MemAvailable from /proc/meminfo, in bytes.
func memAvailable(env *Env) uint64 {
	data, err := env.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "MemAvailable:"); ok {
			fields := strings.Fields(value)
			if len(fields) > 0 {
				kib, _ := strconv.ParseUint(fields[0], 10, 64)
				return kib * 1024
			}
		}
	}
	return 0
}

const gib = 1 << 30

// thermal reads the CPU sensors the safeguard needs.
type thermal struct {
	pkg, maxCore int
	tjmax        int
}

// cpuThermal reads coretemp (Intel) or k10temp (AMD). tjmax is the package
// critical temperature, 95 °C when the sensor does not report one.
func cpuThermal(env *Env) (thermal, bool) {
	t := thermal{tjmax: 95}
	found := false
	inputs, _ := env.Glob("/sys/class/hwmon/hwmon*/temp*_input")
	for _, input := range inputs {
		dir := filepath.Dir(input)
		chip := readTrimmed(env, filepath.Join(dir, "name"))
		if chip != "coretemp" && chip != "k10temp" {
			continue
		}
		base := strings.TrimSuffix(filepath.Base(input), "_input")
		value, err := strconv.Atoi(readTrimmed(env, input))
		if err != nil {
			continue
		}
		value /= 1000
		label := readTrimmed(env, filepath.Join(dir, base+"_label"))
		switch {
		case strings.HasPrefix(label, "Package") || label == "Tctl" || label == "Tdie":
			t.pkg = value
			found = true
			if crit, err := strconv.Atoi(readTrimmed(env, filepath.Join(dir, base+"_crit"))); err == nil && crit > 0 {
				t.tjmax = crit / 1000
			}
		default:
			if value > t.maxCore {
				t.maxCore = value
			}
			found = true
		}
	}
	if t.pkg == 0 {
		t.pkg = t.maxCore
	}
	return t, found
}

func averageMHz(env *Env) (int, bool) {
	data, err := env.ReadFile("/proc/cpuinfo")
	if err != nil {
		return 0, false
	}
	var sum float64
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) == "cpu MHz" {
			if mhz, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
				sum += mhz
				n++
			}
		}
	}
	if n == 0 {
		return 0, false
	}
	return int(sum / float64(n)), true
}

// throttleCount sums the per-core and package thermal throttle counters.
func throttleCount(env *Env) (int, bool) {
	files, _ := env.Glob("/sys/devices/system/cpu/cpu*/thermal_throttle/core_throttle_count")
	pkg, _ := env.Glob("/sys/devices/system/cpu/cpu0/thermal_throttle/package_throttle_count")
	files = append(files, pkg...)
	total, found := 0, false
	for _, file := range files {
		if n, err := strconv.Atoi(readTrimmed(env, file)); err == nil {
			total += n
			found = true
		}
	}
	return total, found
}

func nvmeTemp(env *Env) (int, bool) {
	for name, value := range hwmonTemps(env) {
		if strings.HasPrefix(name, "nvme/") {
			return value, true
		}
	}
	return 0, false
}

// baseMHz is the nominal frequency: intel_pstate's base_frequency, else the
// highest scaling frequency.
func baseMHz(env *Env) int {
	for _, file := range []string{"/sys/devices/system/cpu/cpu0/cpufreq/base_frequency", "/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq"} {
		if khz, err := strconv.Atoi(readTrimmed(env, file)); err == nil && khz > 0 {
			return khz / 1000
		}
	}
	return 0
}

// loadMonitor samples telemetry while a load test runs, emits it, keeps the
// peaks and cancels the load when a safeguard trips: the CPU at Tjmax−5 °C
// for two samples in a row, or line power lost during a test that needs it.
type loadMonitor struct {
	mu             sync.Mutex
	peakPkg        int
	peakCore       int
	mhzSum, mhzN   int
	throttleStart  int
	throttleEvents int
	nvmeBefore     int
	nvmePeak       int
	abortReason    string // "thermal" or "power"
	abortDetail    string
}

func startLoadMonitor(ctx context.Context, env *Env, requiresAC bool, cancel context.CancelFunc) (*loadMonitor, func()) {
	m := &loadMonitor{}
	m.throttleStart, _ = throttleCount(env)
	m.nvmeBefore, _ = nvmeTemp(env)
	interval := env.SampleInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		hot := 0
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			sample := Sample{}
			if t, ok := cpuThermal(env); ok {
				sample.PkgTempC, sample.MaxCoreTempC = &t.pkg, &t.maxCore
				m.mu.Lock()
				m.peakPkg = max(m.peakPkg, t.pkg)
				m.peakCore = max(m.peakCore, t.maxCore)
				m.mu.Unlock()
				if t.pkg >= t.tjmax-5 {
					hot++
				} else {
					hot = 0
				}
				if hot >= 2 {
					m.abort("thermal", strconv.Itoa(t.pkg)+" °C, Tjmax "+strconv.Itoa(t.tjmax)+" °C")
					cancel()
				}
			}
			if mhz, ok := averageMHz(env); ok {
				sample.AvgMHz = &mhz
				m.mu.Lock()
				m.mhzSum += mhz
				m.mhzN++
				m.mu.Unlock()
			}
			if count, ok := throttleCount(env); ok {
				events := count - m.throttleStart
				sample.ThrottleEvents = &events
				m.mu.Lock()
				m.throttleEvents = events
				m.mu.Unlock()
			}
			if temp, ok := nvmeTemp(env); ok {
				sample.NvmeTempC = &temp
				m.mu.Lock()
				m.nvmePeak = max(m.nvmePeak, temp)
				m.mu.Unlock()
			}
			if fields := strings.Fields(readTrimmed(env, "/proc/loadavg")); len(fields) > 0 {
				if load, err := strconv.ParseFloat(fields[0], 64); err == nil {
					sample.Load1 = &load
				}
			}
			if requiresAC && !onLinePower(env) {
				m.abort("power", "")
				cancel()
			}
			if env.Sample != nil {
				env.Sample(sample)
			}
		}
	}()
	return m, func() { close(done); <-stopped }
}

func (m *loadMonitor) abort(reason, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.abortReason == "" {
		m.abortReason, m.abortDetail = reason, detail
	}
}

func (m *loadMonitor) avgMHz() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mhzN == 0 {
		return 0
	}
	return m.mhzSum / m.mhzN
}

// abortFinding turns a tripped safeguard into the stage verdict.
func (m *loadMonitor) abortResult(env *Env, result *StageResult) bool {
	m.mu.Lock()
	reason, detail := m.abortReason, m.abortDetail
	m.mu.Unlock()
	switch reason {
	case "thermal":
		result.Verdict = Aborted
		result.Summary = env.t("diagnostics.summary.aborted-thermal")
		result.Findings = append(result.Findings, env.finding(SeverityAttention, "thermal.abort", detail))
	case "power":
		result.Verdict = Aborted
		result.Summary = env.t("diagnostics.summary.aborted-power")
		result.Findings = append(result.Findings, env.finding(SeverityAttention, "power.abort", ""))
	default:
		return false
	}
	return true
}
