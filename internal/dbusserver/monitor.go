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
	GPUPercent   float64
	GPUPerDevice []float64
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
	metrics.GPUPercent, metrics.GPUPerDevice = m.gpuPercents()
	fillMemory(&metrics)
	metrics.DiskReadBytes, metrics.DiskWriteBytes = diskCounters()
	metrics.NetRxBytes, metrics.NetTxBytes = networkCounters()
	return metrics, nil
}

// gpuPercents reads every GPU exposed by the kernel or vendor tooling. The
// aggregate value remains the busiest device, while the ordered slice lets
// clients render one history graph per GPU.
func (m *MonitorService) gpuPercents() (float64, []float64) {
	percents := gpuDevicesFromDRM("/sys/class/drm")
	for device, percent := range gpuPercentsFromDRM("/sys/class/drm") {
		mergeGPUPercent(percents, device, percent)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	for device, percent := range nvidiaGPUPercentages(ctx) {
		mergeGPUPercent(percents, device, percent)
	}

	// Intel and several other DRM drivers publish cumulative per-client engine
	// times in /proc/*/fdinfo instead of a global sysfs percentage. Keep the
	// previous snapshot on the service so normal two-second monitor refreshes
	// provide the sampling interval without adding another sleep here.
	m.gpuMu.Lock()
	now := time.Now()
	current := drmFDInfoSnapshot("/proc")
	if len(current) > 0 && len(m.gpuPrevious) > 0 {
		preserveMonotonicDRMCounters(m.gpuPrevious, current)
		for device, percent := range gpuPercentsBetween(m.gpuPrevious, current, now.Sub(m.gpuPreviousAt)) {
			mergeGPUPercent(percents, device, percent)
		}
	}
	m.gpuPrevious = current
	m.gpuPreviousAt = now
	m.gpuMu.Unlock()

	if len(percents) == 0 {
		return -1, nil
	}
	devices := make([]string, 0, len(percents))
	for device := range percents {
		devices = append(devices, device)
	}
	sort.Strings(devices)
	values := make([]float64, 0, len(devices))
	aggregate := -1.0
	for _, device := range devices {
		value := percents[device]
		values = append(values, value)
		aggregate = max(aggregate, value)
	}
	return aggregate, values
}

func mergeGPUPercent(percents map[string]float64, device string, percent float64) {
	device = normalizeGPUDevice(device)
	if previous, ok := percents[device]; !ok || percent > previous {
		percents[device] = percent
	}
}

func normalizeGPUDevice(device string) string {
	device = strings.ToLower(strings.TrimSpace(device))
	if domain, rest, ok := strings.Cut(device, ":"); ok && len(domain) > 4 {
		domain = domain[len(domain)-4:]
		device = domain + ":" + rest
	}
	return device
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
	percents := gpuPercentsBetween(first, second, elapsed)
	maximum := 0.0
	for _, percent := range percents {
		maximum = max(maximum, percent)
	}
	return maximum, len(percents) > 0
}

func gpuPercentsBetween(first, second drmEngineSnapshot, elapsed time.Duration) map[string]float64 {
	percents := make(map[string]float64)
	if elapsed <= 0 {
		return percents
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
	for deviceAndEngine, busy := range busyByEngine {
		device, _, _ := strings.Cut(deviceAndEngine, "\x00")
		value := float64(busy) * 100 / float64(elapsed.Nanoseconds())
		mergeGPUPercent(percents, device, min(value, 100))
	}
	return percents
}

func gpuPercentsFromDRM(root string) map[string]float64 {
	percents := make(map[string]float64)
	paths, _ := filepath.Glob(filepath.Join(root, "card[0-9]*", "device", "gpu_busy_percent"))
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
		deviceDir := filepath.Dir(path)
		device := gpuDeviceIdentity(deviceDir)
		mergeGPUPercent(percents, device, value)
	}
	return percents
}

func gpuDevicesFromDRM(root string) map[string]float64 {
	devices := make(map[string]float64)
	paths, _ := filepath.Glob(filepath.Join(root, "card[0-9]*"))
	for _, path := range paths {
		name := filepath.Base(path)
		if _, err := strconv.ParseUint(strings.TrimPrefix(name, "card"), 10, 32); err != nil {
			continue
		}
		deviceDir := filepath.Join(path, "device")
		if _, err := os.Stat(deviceDir); err != nil {
			continue
		}
		devices[gpuDeviceIdentity(deviceDir)] = -1
	}
	return devices
}

func gpuDeviceIdentity(deviceDir string) string {
	data, err := os.ReadFile(filepath.Join(deviceDir, "uevent"))
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if value, ok := strings.CutPrefix(line, "PCI_SLOT_NAME="); ok {
				return normalizeGPUDevice(value)
			}
		}
	}
	return filepath.Base(filepath.Dir(deviceDir))
}

func nvidiaGPUPercentages(ctx context.Context) map[string]float64 {
	path, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return map[string]float64{}
	}
	output, err := exec.CommandContext(
		ctx,
		path,
		"--query-gpu=pci.bus_id,utilization.gpu",
		"--format=csv,noheader,nounits",
	).Output()
	if err != nil {
		return map[string]float64{}
	}
	return parseNvidiaGPUPercentages(string(output))
}

func parseNvidiaGPUPercentages(output string) map[string]float64 {
	percents := make(map[string]float64)
	for _, line := range strings.Split(string(output), "\n") {
		device, rawValue, ok := strings.Cut(line, ",")
		if !ok {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(rawValue), 64)
		if err != nil {
			continue
		}
		value = min(max(value, 0), 100)
		mergeGPUPercent(percents, device, value)
	}
	return percents
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
