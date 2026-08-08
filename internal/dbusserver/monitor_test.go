package dbusserver

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sampleProcStat = `cpu  100 0 100 700 50 0 0 0 0 0
cpu0  50 0 50 350 25 0 0 0 0 0
cpu1  50 0 50 350 25 0 0 0 0 0
intr 12345 0 0 0
ctxt 6789
`

func TestParseProcStatSeparatesAggregateFromPerCoreLines(t *testing.T) {
	aggregate, cores, ok := parseProcStat(sampleProcStat)
	if !ok {
		t.Fatal("expected parseProcStat to succeed")
	}
	if len(cores) != 2 {
		t.Fatalf("expected 2 cores, got %d: %+v", len(cores), cores)
	}
	// idle (700) + iowait (50) = 750; user+nice+system+idle+iowait = 950.
	if aggregate.total != 950 || aggregate.idle != 750 {
		t.Fatalf("unexpected aggregate stat: %+v", aggregate)
	}
	if cores[0].total != 475 || cores[0].idle != 375 {
		t.Fatalf("unexpected cpu0 stat: %+v", cores[0])
	}
}

func TestParseProcStatStopsAtFirstNonCPULine(t *testing.T) {
	_, cores, ok := parseProcStat("cpu  1 2 3 4 5\nintr 1\ncpu0  1 2 3 4 5\n")
	if !ok {
		t.Fatal("expected parseProcStat to succeed")
	}
	if len(cores) != 0 {
		t.Fatalf("expected the cpu0 line after intr to be ignored, got %+v", cores)
	}
}

func TestCPUStatPercentComputesUsageFromDelta(t *testing.T) {
	first := cpuStat{total: 1000, idle: 800}
	second := cpuStat{total: 1100, idle: 850}
	// delta total=100, delta idle=50 -> 50% busy.
	if percent := cpuStatPercent(first, second); percent != 50 {
		t.Fatalf("expected 50%%, got %v", percent)
	}
}

func TestGPUPercentFromDRMReturnsEveryCard(t *testing.T) {
	root := t.TempDir()
	for card, value := range map[string]string{"card0": "17\n", "card1": "63\n"} {
		device := filepath.Join(root, card, "device")
		if err := os.MkdirAll(device, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(device, "gpu_busy_percent"), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	percents := gpuPercentsFromDRM(root)
	if percents["card0"] != 17 || percents["card1"] != 63 || len(percents) != 2 {
		t.Fatalf("expected both GPU percentages, got %+v", percents)
	}
}

func TestGPUPercentFromDRMReportsUnavailable(t *testing.T) {
	if percents := gpuPercentsFromDRM(t.TempDir()); len(percents) != 0 {
		t.Fatalf("expected no available GPU metric, got %+v", percents)
	}
}

func TestGPUDevicesFromDRMKeepsCardsWithoutUtilizationCounter(t *testing.T) {
	root := t.TempDir()
	for _, card := range []string{"card0", "card1", "card1-HDMI-A-1"} {
		if err := os.MkdirAll(filepath.Join(root, card, "device"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	devices := gpuDevicesFromDRM(root)
	if devices["card0"] != -1 || devices["card1"] != -1 || len(devices) != 2 {
		t.Fatalf("expected both physical cards and no connector, got %+v", devices)
	}
}

func TestParseNvidiaGPUPercentagesKeepsDevicesSeparate(t *testing.T) {
	percents := parseNvidiaGPUPercentages("00000000:01:00.0, 28\n00000000:02:00.0, 74\n")
	if percents["0000:01:00.0"] != 28 || percents["0000:02:00.0"] != 74 || len(percents) != 2 {
		t.Fatalf("expected both NVIDIA GPUs with normalized PCI addresses, got %+v", percents)
	}
}

func TestParseDRMFDInfoReadsEngineNanoseconds(t *testing.T) {
	client, engines, ok := parseDRMFDInfo(`pos: 0
drm-driver: i915
drm-client-id: 7
drm-pdev: 0000:00:02.0
drm-engine-render: 120000000 ns
drm-engine-video: 30000000 ns
drm-engine-capacity-video: 2
`)
	if !ok || client != "0000:00:02.0\x007" {
		t.Fatalf("unexpected client identity %q (found=%v)", client, ok)
	}
	if engines["render"] != 120000000 || engines["video"] != 30000000 || len(engines) != 2 {
		t.Fatalf("unexpected DRM engines: %+v", engines)
	}
}

func TestGPUPercentBetweenUsesBusiestEngine(t *testing.T) {
	first := drmEngineSnapshot{
		"0000:00:02.0\x001": {"render": 100, "video": 200},
		"0000:00:02.0\x002": {"render": 300},
	}
	second := drmEngineSnapshot{
		"0000:00:02.0\x001": {"render": 25000100, "video": 10000200},
		"0000:00:02.0\x002": {"render": 25000300},
	}
	percent, ok := gpuPercentBetween(first, second, 100*time.Millisecond)
	if !ok || percent != 50 {
		t.Fatalf("expected render engine at 50%%, got %v (found=%v)", percent, ok)
	}
}

func TestGPUPercentBetweenKeepsDevicesSeparate(t *testing.T) {
	first := drmEngineSnapshot{
		"0000:00:02.0\x001": {"render": 0},
		"0000:03:00.0\x001": {"render": 0},
	}
	second := drmEngineSnapshot{
		"0000:00:02.0\x001": {"render": 25000000},
		"0000:03:00.0\x001": {"render": 75000000},
	}
	percents := gpuPercentsBetween(first, second, 100*time.Millisecond)
	if percents["0000:00:02.0"] != 25 || percents["0000:03:00.0"] != 75 || len(percents) != 2 {
		t.Fatalf("expected a percentage per DRM device, got %+v", percents)
	}
}
