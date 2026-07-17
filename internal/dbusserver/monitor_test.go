package dbusserver

import "testing"

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
