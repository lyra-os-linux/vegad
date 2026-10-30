package dbusserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/diagnostics"
)

func weekReport(job uint32, findings []diagnostics.Finding, wear float64) *diagnosticReport {
	return &diagnosticReport{Schema: 1, JobID: job, Locale: "en-US", Stages: []diagnostics.StageResult{
		{ID: "disk-health", Verdict: diagnostics.Ok, Summary: "SMART ok", Metrics: map[string]any{"/dev/nvme0n1": map[string]any{"percentUsed": wear}}},
		{ID: "security", Verdict: diagnostics.Attention, Summary: "1", Findings: findings},
	}}
}

var port8000 = diagnostics.Finding{Severity: diagnostics.SeverityAttention, Code: "security.exposed-port", Text: "python3 port 8000", RemedyArgs: []string{"public|port:8000/tcp"}}
var port9000 = diagnostics.Finding{Severity: diagnostics.SeverityAttention, Code: "security.exposed-port", Text: "python3 port 9000", RemedyArgs: []string{"public|port:9000/tcp"}}
var patches = diagnostics.Finding{Severity: diagnostics.SeverityAttention, Code: patchesCode, Text: "2 security patches pending"}
var noise = diagnostics.Finding{Severity: diagnostics.Info, Code: "platform.secure-boot-off", Text: "Secure Boot off"}

func TestPeriodicAnnouncesOnlyWhatIsNew(t *testing.T) {
	week := 7 * 24 * time.Hour
	start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

	state, news := comparePeriodic(loadPeriodicState(filepath.Join(t.TempDir(), "none")), weekReport(1, []diagnostics.Finding{port8000, noise}, 2), start)
	if len(news) != 1 || news[0] != "python3 port 8000" {
		t.Fatalf("first run: %v", news)
	}
	// Same findings a week later: nothing to say.
	state, news = comparePeriodic(state, weekReport(2, []diagnostics.Finding{port8000, noise}, 2), start.Add(week))
	if len(news) != 0 {
		t.Fatalf("unchanged week: %v", news)
	}
	// A second exposed port is news even though the code is known.
	state, news = comparePeriodic(state, weekReport(3, []diagnostics.Finding{port8000, port9000}, 2), start.Add(2*week))
	if len(news) != 1 || news[0] != "python3 port 9000" {
		t.Fatalf("new port: %v", news)
	}
	// A finding that went away and comes back is news again.
	state, _ = comparePeriodic(state, weekReport(4, nil, 2), start.Add(3*week))
	if _, news = comparePeriodic(state, weekReport(5, []diagnostics.Finding{port8000}, 2), start.Add(4*week)); len(news) != 1 {
		t.Fatalf("returned finding: %v", news)
	}
	if state.JobID != 4 || len(state.Stages) != 2 || state.Stages[1][0] != "security" {
		t.Fatalf("state %+v", state)
	}
}

func TestPeriodicPatchesAreNewsAfterAWeekOnlyOnce(t *testing.T) {
	day := 24 * time.Hour
	start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	state, news := comparePeriodic(periodicState{}, weekReport(1, []diagnostics.Finding{patches}, 2), start)
	if len(news) != 0 {
		t.Fatalf("patches just arrived: %v", news)
	}
	state, news = comparePeriodic(state, weekReport(2, []diagnostics.Finding{patches}, 2), start.Add(8*day))
	if len(news) != 1 || !state.PatchesNotified {
		t.Fatalf("overdue patches: %v", news)
	}
	state, news = comparePeriodic(state, weekReport(3, []diagnostics.Finding{patches}, 2), start.Add(15*day))
	if len(news) != 0 {
		t.Fatalf("announced twice: %v", news)
	}
	// Installed, then new ones arrive: the notice can come again later.
	state, _ = comparePeriodic(state, weekReport(4, nil, 2), start.Add(16*day))
	if state.PatchesNotified {
		t.Fatal("notice not reset after the patches were installed")
	}
}

func TestPeriodicWearRiseWithinThirtyDays(t *testing.T) {
	week := 7 * 24 * time.Hour
	start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	state, _ := comparePeriodic(periodicState{}, weekReport(1, nil, 10), start)
	state, news := comparePeriodic(state, weekReport(2, nil, 13), start.Add(week))
	if len(news) != 0 {
		t.Fatalf("3 points: %v", news)
	}
	state, news = comparePeriodic(state, weekReport(3, nil, 16), start.Add(2*week))
	if len(news) != 1 || !strings.Contains(news[0], "10%") || !strings.Contains(news[0], "16%") {
		t.Fatalf("6 points in 14 days: %v", news)
	}
	if _, news = comparePeriodic(state, weekReport(4, nil, 23), start.Add(3*week)); len(news) != 0 {
		t.Fatalf("announced again within the month: %v", news)
	}
}

func TestPeriodicStateRoundTripsAndFeedsLastPeriodic(t *testing.T) {
	s := fakeDiagnostics(t, nil)
	s.periodicPath = filepath.Join(t.TempDir(), "periodic.json")
	if job, _, count, stages, texts, err := s.LastPeriodic(); err != nil || job != 0 || count != 0 || len(stages) != 0 || texts == nil {
		t.Fatalf("before any run: %d %d %v %v %v", job, count, stages, texts, err)
	}
	s.afterPeriodic(&runningDiagnostic{id: 9, periodic: true}, weekReport(9, []diagnostics.Finding{port8000}, 2))
	job, finished, count, stages, texts, err := s.LastPeriodic()
	if err != nil || job != 9 || finished == 0 || count != 1 || len(stages) != 2 || texts[0] != "python3 port 8000" {
		t.Fatalf("%d %d %d %v %v %v", job, finished, count, stages, texts, err)
	}
}

func TestPeriodicStartIsRootOnlyQuietWhenBusyAndOnLowBattery(t *testing.T) {
	s := fakeDiagnostics(t, map[string]diagnostics.TestFunc{"disk-health": okStage("disk-health"), "cpu-stress": okStage("cpu-stress")})
	periodic := map[string]dbus.Variant{"periodic": dbus.MakeVariant(true)}
	// No bus connection to prove the caller is root: refused.
	if _, err := s.Start(":1.42", periodicTests, periodic); err == nil || err.Name != BusName+".Error.AuthorizationFailed" {
		t.Fatalf("non-root periodic start: %v", err)
	}
	s.lowBattery = func() bool { return true }
	if job, err := s.start([]string{"disk-health"}, periodic); job != 0 || err != nil {
		t.Fatalf("low battery: %d %v", job, err)
	}
	s.lowBattery = func() bool { return false }
	release, _ := operations.beginTransaction()
	defer release()
	if job, err := s.start([]string{"disk-health"}, periodic); job != 0 || err != nil {
		t.Fatalf("busy: %d %v", job, err)
	}
	if _, err := s.start([]string{"cpu-stress"}, periodic); err == nil {
		t.Fatal("a load test in the weekly check")
	}
}

func TestPeriodicFrequencyAndSwitch(t *testing.T) {
	dir := t.TempDir()
	saved, savedRun := periodicDropIn, systemctlRun
	periodicDropIn = filepath.Join(dir, "timer.d", "10-vega-frequency.conf")
	var calls []string
	enabled := "disabled"
	systemctlRun = func(args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "enable":
			enabled = "enabled"
		case "disable":
			enabled = "disabled"
		case "is-enabled":
			return []byte(enabled + "\n"), nil
		}
		return nil, nil
	}
	t.Cleanup(func() { periodicDropIn, systemctlRun = saved, savedRun })

	if err := setPeriodic(true, "daily"); err != nil {
		t.Fatal(err)
	}
	if on, frequency := periodicSettings(); !on || frequency != "daily" {
		t.Fatalf("%v %s", on, frequency)
	}
	data, _ := os.ReadFile(periodicDropIn)
	if !strings.Contains(string(data), "OnCalendar=\nOnCalendar=daily") {
		t.Fatalf("drop-in %q", data)
	}
	if err := setPeriodic(false, "weekly"); err != nil {
		t.Fatal(err)
	}
	if on, frequency := periodicSettings(); on || frequency != "weekly" {
		t.Fatalf("%v %s", on, frequency)
	}
	if err := setPeriodic(true, "hourly"); err == nil {
		t.Fatal("unknown frequency accepted")
	}
	if strings.Join(calls, ";") != "daemon-reload;enable --now vegad-health-check.timer;is-enabled vegad-health-check.timer;daemon-reload;disable --now vegad-health-check.timer;is-enabled vegad-health-check.timer" {
		t.Fatalf("%v", calls)
	}
}

func TestLowBatteryNeedsBatteryUnderThirtyWithoutCharger(t *testing.T) {
	saved := powerSupplyDir
	t.Cleanup(func() { powerSupplyDir = saved })
	write := func(dir, name, value string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		_ = os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o644)
	}
	for _, tc := range []struct {
		capacity, online string
		low              bool
	}{{"20", "0", true}, {"20", "1", false}, {"45", "0", false}} {
		dir := t.TempDir()
		powerSupplyDir = dir
		_ = os.MkdirAll(filepath.Join(dir, "BAT1"), 0o755)
		_ = os.MkdirAll(filepath.Join(dir, "ACAD"), 0o755)
		write(dir, "BAT1/type", "Battery")
		write(dir, "BAT1/capacity", tc.capacity)
		write(dir, "ACAD/type", "Mains")
		write(dir, "ACAD/online", tc.online)
		if got := lowBattery(); got != tc.low {
			t.Errorf("%+v: %v", tc, got)
		}
	}
	powerSupplyDir = t.TempDir() // a desktop without battery
	if lowBattery() {
		t.Fatal("desktop counted as low battery")
	}
}
