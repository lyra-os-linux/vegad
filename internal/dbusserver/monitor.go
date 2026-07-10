package dbusserver

import (
	"fmt"
	"os"
	"os/user"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

type MonitorService struct {
	activity *Activity
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
}

type ProcessInfo struct {
	PID        uint32
	Name       string
	User       string
	CPUPercent float64
	Memory     uint64
	State      string
}

func (m *MonitorService) Metrics() (SystemMetrics, *dbus.Error) {
	m.activity.Touch()
	metrics := SystemMetrics{}
	metrics.CPUPercent = cpuPercentSnapshot()
	fillMemory(&metrics)
	metrics.DiskReadBytes, metrics.DiskWriteBytes = diskCounters()
	metrics.NetRxBytes, metrics.NetTxBytes = networkCounters()
	return metrics, nil
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

func cpuPercentSnapshot() float64 {
	first, ok := readCPUStat()
	if !ok {
		return 0
	}
	time.Sleep(120 * time.Millisecond)
	second, ok := readCPUStat()
	if !ok {
		return 0
	}
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

func readCPUStat() (cpuStat, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuStat{}, false
	}
	line := strings.SplitN(string(data), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return cpuStat{}, false
	}
	var total uint64
	for _, field := range fields[1:] {
		value, _ := strconv.ParseUint(field, 10, 64)
		total += value
	}
	idle, _ := strconv.ParseUint(fields[4], 10, 64)
	if len(fields) > 5 {
		iowait, _ := strconv.ParseUint(fields[5], 10, 64)
		idle += iowait
	}
	return cpuStat{total: total, idle: idle}, true
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
	return ProcessInfo{PID: pid, Name: name, User: username, CPUPercent: cpu, Memory: mem, State: state}, true
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
