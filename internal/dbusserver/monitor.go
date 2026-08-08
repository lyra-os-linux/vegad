package dbusserver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

type MonitorService struct {
	activity      *Activity
	gpuMu         sync.Mutex
	gpuPrevious   drmEngineSnapshot
	gpuPreviousAt time.Time
}

type SystemMetrics struct {
	CPUPercent     float64
	MemUsed        uint64
	MemTotal       uint64
	SwapUsed       uint64
	SwapTotal      uint64
	DiskReadBytes  uint64
	DiskWriteBytes uint64
	NetRxBytes     uint64
	NetTxBytes     uint64
	CPUPerCore     []float64
	// GPUPercent is -1 when neither sysfs nor the NVIDIA tooling exposes
	// utilization for an installed GPU.
	GPUPercent float64
}

type ProcessInfo struct {
	PID        uint32
	PPID       uint32
	Name       string
	User       string
	CPUPercent float64
	Memory     uint64
	State      string
}

func (m *MonitorService) Metrics() (SystemMetrics, *dbus.Error) {
	m.activity.Touch()
	metrics := SystemMetrics{}
	metrics.CPUPercent, metrics.CPUPerCore = cpuPercentSnapshot()
	metrics.GPUPercent = m.gpuPercent()
	fillMemory(&metrics)
	metrics.DiskReadBytes, metrics.DiskWriteBytes = diskCounters()
	metrics.NetRxBytes, metrics.NetTxBytes = networkCounters()
	return metrics, nil
}

// gpuPercent reads the kernel's inexpensive utilization counter when the DRM
// driver provides one (notably amdgpu), then falls back to nvidia-smi and DRM
// per-client engine counters. With more than one GPU, the busiest available
// device is what the system-wide card displays.
func (m *MonitorService) gpuPercent() float64 {
	percent, found := gpuPercentFromDRM("/sys/class/drm")
	if !found {
		percent = -1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	if nvidiaPercent, ok := nvidiaGPUPercent(ctx); ok && nvidiaPercent > percent {
		percent = nvidiaPercent
	}

	// Intel and several other DRM drivers publish cumulative per-client engine
	// times in /proc/*/fdinfo instead of a global sysfs percentage. Keep the
	// previous snapshot on the service so normal two-second monitor refreshes
	// provide the sampling interval without adding another sleep here.
	if percent < 0 {
		m.gpuMu.Lock()
		now := time.Now()
		current := drmFDInfoSnapshot("/proc")
		if len(current) > 0 && len(m.gpuPrevious) > 0 {
			preserveMonotonicDRMCounters(m.gpuPrevious, current)
			if drmPercent, ok := gpuPercentBetween(m.gpuPrevious, current, now.Sub(m.gpuPreviousAt)); ok {
				percent = drmPercent
			}
		}
		m.gpuPrevious = current
		m.gpuPreviousAt = now
		m.gpuMu.Unlock()
	}
	return percent
}

type drmEngineSnapshot map[string]map[string]uint64

func drmFDInfoSnapshot(root string) drmEngineSnapshot {
	snapshot := make(drmEngineSnapshot)
	processes, err := os.ReadDir(root)
	if err != nil {
		return snapshot
	}
	for _, process := range processes {
		if _, err := strconv.ParseUint(process.Name(), 10, 32); err != nil {
			continue
		}
		fdinfoDir := filepath.Join(root, process.Name(), "fdinfo")
		fds, err := os.ReadDir(fdinfoDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			data, err := os.ReadFile(filepath.Join(fdinfoDir, fd.Name()))
			if err != nil {
				continue
			}
			client, engines, ok := parseDRMFDInfo(string(data))
			if ok {
				// Duplicated file descriptors have the same DRM client id and
				// counters; assignment avoids counting those clients twice.
				snapshot[client] = engines
			}
		}
	}
	return snapshot
}

func parseDRMFDInfo(data string) (string, map[string]uint64, bool) {
	var driver, device, client string
	engines := make(map[string]uint64)
	for _, line := range strings.Split(data, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "drm-driver":
			driver = value
		case "drm-pdev":
			device = value
		case "drm-client-id":
			client = value
		default:
			if !strings.HasPrefix(key, "drm-engine-") || strings.HasPrefix(key, "drm-engine-capacity-") {
				continue
			}
			fields := strings.Fields(value)
			if len(fields) == 0 || (len(fields) > 1 && fields[1] != "ns") {
				continue
			}
			busy, err := strconv.ParseUint(fields[0], 10, 64)
			if err == nil {
				engines[strings.TrimPrefix(key, "drm-engine-")] = busy
			}
		}
	}
	if client == "" || len(engines) == 0 {
		return "", nil, false
	}
	if device == "" {
		device = driver
	}
	return device + "\x00" + client, engines, true
}

func preserveMonotonicDRMCounters(previous, current drmEngineSnapshot) {
	for client, currentEngines := range current {
		previousEngines, ok := previous[client]
		if !ok {
			continue
		}
		for engine, currentValue := range currentEngines {
			if previousValue, ok := previousEngines[engine]; ok && currentValue < previousValue {
				currentEngines[engine] = previousValue
			}
		}
	}
}

func gpuPercentBetween(first, second drmEngineSnapshot, elapsed time.Duration) (float64, bool) {
	if elapsed <= 0 {
		return 0, false
	}
	busyByEngine := make(map[string]uint64)
	for client, secondEngines := range second {
		firstEngines, ok := first[client]
		if !ok {
			continue
		}
		device, _, _ := strings.Cut(client, "\x00")
		for engine, secondValue := range secondEngines {
			firstValue, ok := firstEngines[engine]
			if !ok || secondValue < firstValue {
				continue
			}
			busyByEngine[device+"\x00"+engine] += secondValue - firstValue
		}
	}
	maximum := 0.0
	for _, busy := range busyByEngine {
		value := float64(busy) * 100 / float64(elapsed.Nanoseconds())
		maximum = max(maximum, min(value, 100))
	}
	return maximum, len(busyByEngine) > 0
}

func gpuPercentFromDRM(root string) (float64, bool) {
	paths, _ := filepath.Glob(filepath.Join(root, "card[0-9]*", "device", "gpu_busy_percent"))
	maximum := 0.0
	found := false
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		if err != nil {
			continue
		}
		value = min(max(value, 0), 100)
		if !found || value > maximum {
			maximum = value
		}
		found = true
	}
	return maximum, found
}

func nvidiaGPUPercent(ctx context.Context) (float64, bool) {
	path, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return 0, false
	}
	output, err := exec.CommandContext(
		ctx,
		path,
		"--query-gpu=utilization.gpu",
		"--format=csv,noheader,nounits",
	).Output()
	if err != nil {
		return 0, false
	}
	maximum := 0.0
	found := false
	for _, line := range strings.Split(string(output), "\n") {
		value, err := strconv.ParseFloat(strings.TrimSpace(line), 64)
		if err != nil {
			continue
		}
		value = min(max(value, 0), 100)
		if !found || value > maximum {
			maximum = value
		}
		found = true
	}
	return maximum, found
}

func (m *MonitorService) ListProcesses() ([]ProcessInfo, *dbus.Error) {
	m.activity.Touch()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	var rows []ProcessInfo
	ticks := clockTicks()
	uptime := uptimeSeconds()
	for _, entry := range entries {
		pid64, err := strconv.ParseUint(entry.Name(), 10, 32)
		if err != nil {
			continue
		}
		if proc, ok := readProcess(uint32(pid64), ticks, uptime); ok {
			rows = append(rows, proc)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].CPUPercent == rows[j].CPUPercent {
			return rows[i].Memory > rows[j].Memory
		}
		return rows[i].CPUPercent > rows[j].CPUPercent
	})
	if len(rows) > 250 {
		rows = rows[:250]
	}
	return rows, nil
}

func (m *MonitorService) KillProcess(sender dbus.Sender, pid uint32) *dbus.Error {
	m.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.monitor.kill"); err != nil {
		return err
	}
	if pid < 2 {
		return dbus.MakeFailedError(fmt.Errorf("PID inválido"))
	}
	if err := syscall.Kill(int(pid), syscall.SIGTERM); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

// cpuPercentSnapshot samples /proc/stat twice 120ms apart, once, and derives
// both the aggregate percentage and every core's from that single window —
// sampling each core separately would need a 120ms sleep per core.
func cpuPercentSnapshot() (float64, []float64) {
	firstTotal, firstCores, ok := readCPUStat()
	if !ok {
		return 0, nil
	}
	time.Sleep(120 * time.Millisecond)
	secondTotal, secondCores, ok := readCPUStat()
	if !ok {
		return 0, nil
	}
	cores := make([]float64, 0, len(firstCores))
	for i := range firstCores {
		if i >= len(secondCores) {
			break
		}
		cores = append(cores, cpuStatPercent(firstCores[i], secondCores[i]))
	}
	return cpuStatPercent(firstTotal, secondTotal), cores
}

func cpuStatPercent(first, second cpuStat) float64 {
	total := float64(second.total - first.total)
	idle := float64(second.idle - first.idle)
	if total <= 0 {
		return 0
	}
	return (total - idle) * 100 / total
}

type cpuStat struct {
	total uint64
	idle  uint64
}

// readCPUStat reads the aggregate "cpu" line plus every per-core "cpuN"
// line — they're always contiguous at the top of /proc/stat, so the first
// line without a "cpu" prefix ends the block.
func readCPUStat() (cpuStat, []cpuStat, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuStat{}, nil, false
	}
	return parseProcStat(string(data))
}

func parseProcStat(data string) (cpuStat, []cpuStat, bool) {
	var aggregate cpuStat
	aggregateSet := false
	var cores []cpuStat
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "cpu") {
			break
		}
		if len(fields) < 5 {
			continue
		}
		stat := parseCPUFields(fields[1:])
		if fields[0] == "cpu" {
			aggregate = stat
			aggregateSet = true
		} else {
			cores = append(cores, stat)
		}
	}
	if !aggregateSet {
		return cpuStat{}, nil, false
	}
	return aggregate, cores, true
}

func parseCPUFields(fields []string) cpuStat {
	var total uint64
	for _, field := range fields {
		value, _ := strconv.ParseUint(field, 10, 64)
		total += value
	}
	idle, _ := strconv.ParseUint(fields[3], 10, 64)
	if len(fields) > 4 {
		iowait, _ := strconv.ParseUint(fields[4], 10, 64)
		idle += iowait
	}
	return cpuStat{total: total, idle: idle}
}

func fillMemory(metrics *SystemMetrics) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseUint(fields[1], 10, 64)
		values[strings.TrimSuffix(fields[0], ":")] = value * 1024
	}
	metrics.MemTotal = values["MemTotal"]
	available := values["MemAvailable"]
	if metrics.MemTotal > available {
		metrics.MemUsed = metrics.MemTotal - available
	}
	metrics.SwapTotal = values["SwapTotal"]
	if metrics.SwapTotal > values["SwapFree"] {
		metrics.SwapUsed = metrics.SwapTotal - values["SwapFree"]
	}
}

func diskCounters() (uint64, uint64) {
	data, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return 0, 0
	}
	var readSectors, writtenSectors uint64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 14 {
			continue
		}
		name := fields[2]
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") {
			continue
		}
		r, _ := strconv.ParseUint(fields[5], 10, 64)
		w, _ := strconv.ParseUint(fields[9], 10, 64)
		readSectors += r
		writtenSectors += w
	}
	return readSectors * 512, writtenSectors * 512
}

func networkCounters() (uint64, uint64) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0
	}
	var rx, tx uint64
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if strings.TrimSpace(parts[0]) == "lo" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 16 {
			continue
		}
		r, _ := strconv.ParseUint(fields[0], 10, 64)
		t, _ := strconv.ParseUint(fields[8], 10, 64)
		rx += r
		tx += t
	}
	return rx, tx
}

func readProcess(pid uint32, ticks float64, uptime float64) (ProcessInfo, bool) {
	statData, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ProcessInfo{}, false
	}
	stat := string(statData)
	endName := strings.LastIndex(stat, ")")
	startName := strings.Index(stat, "(")
	if startName < 0 || endName < 0 || endName <= startName {
		return ProcessInfo{}, false
	}
	name := stat[startName+1 : endName]
	fields := strings.Fields(stat[endName+2:])
	if len(fields) < 22 {
		return ProcessInfo{}, false
	}
	ppid64, _ := strconv.ParseUint(fields[1], 10, 32)
	utime, _ := strconv.ParseFloat(fields[11], 64)
	stime, _ := strconv.ParseFloat(fields[12], 64)
	starttime, _ := strconv.ParseFloat(fields[19], 64)
	seconds := uptime - (starttime / ticks)
	cpu := 0.0
	if seconds > 0 {
		cpu = ((utime + stime) / ticks) / seconds * 100
	}
	statusData, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	uid := ""
	state := ""
	mem := uint64(0)
	for _, line := range strings.Split(string(statusData), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "Uid":
			uid = fields[1]
		case "State":
			state = strings.Join(fields[1:], " ")
		case "VmRSS":
			value, _ := strconv.ParseUint(fields[1], 10, 64)
			mem = value * 1024
		}
	}
	username := uid
	if u, err := user.LookupId(uid); err == nil {
		username = u.Username
	}
	return ProcessInfo{PID: pid, PPID: uint32(ppid64), Name: name, User: username, CPUPercent: cpu, Memory: mem, State: state}, true
}

func uptimeSeconds() float64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 1
	}
	fields := strings.Fields(string(data))
	value, _ := strconv.ParseFloat(fields[0], 64)
	return value
}

func clockTicks() float64 {
	return 100
}
