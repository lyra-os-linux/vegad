package dbusserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/diagnostics"
)

// fakeDiagnostics wires a service to scripted tests and a temporary store.
func fakeDiagnostics(t *testing.T, tests map[string]diagnostics.TestFunc) *DiagnosticsService {
	t.Helper()
	saved := operations
	operations = newTestGate(false)
	t.Cleanup(func() { operations = saved })
	s := newDiagnosticsService(&Activity{last: time.Now()}, nil)
	s.store = &diagnosticStore{dir: t.TempDir(), keep: 10}
	s.seen = logsSeen{path: filepath.Join(s.store.dir, "logs-seen.json")}
	s.backups = nil
	s.newEnv = func(locale string, bootsBack int) *diagnostics.Env {
		return &diagnostics.Env{
			Locale: locale, BootsBack: bootsBack,
			ReadFile: func(path string) ([]byte, error) {
				if path == "/sys/class/dmi/id/product_name" {
					return []byte("Nitro AN515-57\n"), nil
				}
				return nil, os.ErrNotExist
			},
			LookPath: func(string) (string, error) { return "/usr/bin/tool", nil },
		}
	}
	s.lookup = func(id string) (diagnostics.Test, bool) {
		run, ok := tests[id]
		if !ok {
			return diagnostics.Test{}, false
		}
		return diagnostics.Test{ID: id, EstimatedSeconds: 1, Run: run}, true
	}
	return s
}

func okStage(id string, findings ...diagnostics.Finding) diagnostics.TestFunc {
	return func(ctx context.Context, env *diagnostics.Env, progress diagnostics.Progress) diagnostics.StageResult {
		progress(50, "halfway")
		return diagnostics.StageResult{ID: id, Verdict: diagnostics.Ok, Summary: "fine", Findings: findings}
	}
}

func waitIdle(t *testing.T, s *DiagnosticsService) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if job, _ := s.Current(); job.JobId == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("diagnostic never finished")
}

func loadReport(t *testing.T, s *DiagnosticsService, id uint32, redact bool) map[string]any {
	t.Helper()
	raw, err := s.report(id, redact)
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestDiagnosticJobRunsStagesInOrderAndKeepsTheReport(t *testing.T) {
	s := fakeDiagnostics(t, map[string]diagnostics.TestFunc{
		"packages": okStage("packages"),
		"logs": func(ctx context.Context, env *diagnostics.Env, progress diagnostics.Progress) diagnostics.StageResult {
			return diagnostics.StageResult{Verdict: diagnostics.Attention, Summary: "crashes", Findings: []diagnostics.Finding{{Severity: diagnostics.SeverityAttention, Code: "logs.coredumps"}}}
		},
	})
	id, err := s.start([]string{"packages", "logs"}, map[string]dbus.Variant{"locale": dbus.MakeVariant("pt_BR")})
	if err != nil || id != 1 {
		t.Fatalf("id = %d, err = %v", id, err)
	}
	waitIdle(t, s)
	report := loadReport(t, s, 0, false)
	stages := report["stages"].([]any)
	if report["schema"] != float64(1) || report["result"] != "attention" || len(stages) != 2 {
		t.Fatalf("report %+v", report)
	}
	if stages[0].(map[string]any)["id"] != "packages" || stages[1].(map[string]any)["id"] != "logs" {
		t.Fatal("stages out of order")
	}
	if host := report["host"].(map[string]any); host["product"] != "Nitro AN515-57" {
		t.Fatalf("host %+v", host)
	}
	if _, ok := report["sensitive"]; ok {
		t.Fatal("the sensitive list must never leave the daemon")
	}
	info, statErr := os.Stat(filepath.Join(s.store.dir, "1.json"))
	if statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("report file: %v %v", info, statErr)
	}
}

func TestDiagnosticStartValidatesTestsAndOptions(t *testing.T) {
	s := fakeDiagnostics(t, map[string]diagnostics.TestFunc{"packages": okStage("packages")})
	cases := []struct {
		tests   []string
		options map[string]dbus.Variant
	}{
		{nil, nil},
		{[]string{"gpu"}, nil},
		{[]string{"packages", "packages"}, nil},
		{[]string{"packages"}, map[string]dbus.Variant{"durationSeconds": dbus.MakeVariant(uint32(5))}},
		{[]string{"packages"}, map[string]dbus.Variant{"memoryPercent": dbus.MakeVariant(uint32(80))}},
		{[]string{"packages"}, map[string]dbus.Variant{"bootsBack": dbus.MakeVariant("8")}},
		{[]string{"packages"}, map[string]dbus.Variant{"locale": dbus.MakeVariant(uint32(1))}},
	}
	for _, c := range cases {
		if _, err := s.start(c.tests, c.options); err == nil || err.Name != BusName+".Error.NotSupported" {
			t.Errorf("tests %v options %v: err = %v", c.tests, c.options, err)
		}
	}
	opts, err := parseDiagnosticOptions(map[string]dbus.Variant{"bootsBack": dbus.MakeVariant(uint32(3)), "future": dbus.MakeVariant(true)})
	if err != nil || opts.bootsBack != 3 || opts.durationSeconds != 240 || opts.memoryPercent != 70 {
		t.Fatalf("opts %+v err %v", opts, err)
	}
}

func TestDiagnosticCancelStopsTheJobAndKeepsAPartialReport(t *testing.T) {
	started := make(chan struct{})
	s := fakeDiagnostics(t, map[string]diagnostics.TestFunc{
		"packages": okStage("packages"),
		"cpu-stress": func(ctx context.Context, env *diagnostics.Env, progress diagnostics.Progress) diagnostics.StageResult {
			close(started)
			<-ctx.Done()
			return diagnostics.StageResult{Verdict: diagnostics.Cancelled}
		},
		"logs": okStage("logs"),
	})
	id, err := s.start([]string{"packages", "cpu-stress", "logs"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	job, _ := s.Current()
	if job.JobId != id || job.Stage != "cpu-stress" || len(job.Tests) != 3 {
		t.Fatalf("current %+v", job)
	}
	if _, busy := s.start([]string{"packages"}, nil); busy == nil || busy.Name != BusName+".Error.Busy" {
		t.Fatalf("second job: %v", busy)
	}
	if _, busy := operations.beginTransaction(); busy == nil {
		t.Fatal("transaction allowed during a diagnostic")
	}
	s.cancel(id + 1) // another job id: ignored
	s.cancel(id)
	waitIdle(t, s)
	report := loadReport(t, s, id, false)
	stages := report["stages"].([]any)
	verdicts := []string{}
	for _, stage := range stages {
		verdicts = append(verdicts, stage.(map[string]any)["verdict"].(string))
	}
	if report["result"] != "cancelled" || strings.Join(verdicts, ",") != "ok,cancelled,cancelled" {
		t.Fatalf("result %v verdicts %v", report["result"], verdicts)
	}
	release, busy := operations.beginTransaction()
	if busy != nil {
		t.Fatal("gate not released after the job")
	}
	release()
}

func TestDiagnosticStageTimeoutComesFromTheEstimate(t *testing.T) {
	if got := stageTimeout(diagnostics.Test{EstimatedSeconds: 10}, &diagnostics.Env{}); got != time.Minute {
		t.Errorf("short test: %s", got)
	}
	if got := stageTimeout(diagnostics.Test{EstimatedSeconds: 240}, &diagnostics.Env{}); got != 8*time.Minute {
		t.Errorf("stress test: %s", got)
	}
}

func TestUnavailableTestsBecomeUnavailableStages(t *testing.T) {
	s := fakeDiagnostics(t, map[string]diagnostics.TestFunc{"packages": okStage("packages")})
	s.lookup = func(id string) (diagnostics.Test, bool) {
		if id == "fs-scrub" {
			return diagnostics.Test{ID: id}, true // Run nil: not implemented
		}
		return diagnostics.Test{ID: id, EstimatedSeconds: 1, Run: okStage(id)}, id == "packages"
	}
	id, err := s.start([]string{"packages", "fs-scrub"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	report := loadReport(t, s, id, false)
	scrub := report["stages"].([]any)[1].(map[string]any)
	if scrub["verdict"] != "unavailable" || scrub["metrics"].(map[string]any)["reason"] != "not-implemented" || report["result"] != "ok" {
		t.Fatalf("report %+v", report)
	}
}

func TestReportRedactsSerialsHostnameAndMACs(t *testing.T) {
	s := fakeDiagnostics(t, nil)
	report := &diagnosticReport{
		Schema: 1, JobID: 4, Started: "now",
		Host:      map[string]string{"hostname": "rodrigo-nitro", "product": "Nitro"},
		Sensitive: []string{"rodrigo-nitro", "KN34291K6QHL"},
		Stages: []diagnostics.StageResult{{ID: "disk-health", Verdict: diagnostics.Ok, Findings: []diagnostics.Finding{
			{Code: "logs.errors", Raw: "r8169 eth0: RTL8168h, 9c:2d:cd:a1:b2:c3, IRQ 140\nconnected to SSID 'CasaRodrigo'\nserial KN34291K6QHL\n1  /home/rodrigo/Git/vega/target/debug/vega-gtk SIGSEGV"},
		}}},
	}
	if err := s.store.save(report); err != nil {
		t.Fatal(err)
	}
	plain, _ := s.report(4, false)
	if !strings.Contains(plain, "KN34291K6QHL") || strings.Contains(plain, "sensitive") {
		t.Fatalf("unredacted report: %s", plain)
	}
	masked, _ := s.report(4, true)
	for _, secret := range []string{"KN34291K6QHL", "rodrigo-nitro", "9c:2d:cd", "CasaRodrigo", "/home/rodrigo"} {
		if strings.Contains(masked, secret) {
			t.Errorf("%s leaked: %s", secret, masked)
		}
	}
	if !strings.Contains(masked, "Nitro") {
		t.Fatal("redaction removed non-sensitive data")
	}
}

func TestReportStoreKeepsTheLastTenAndContinuesIds(t *testing.T) {
	dir := t.TempDir()
	store := &diagnosticStore{dir: dir, keep: 10}
	for i := 0; i < 12; i++ {
		id, err := store.nextID()
		if err != nil {
			t.Fatal(err)
		}
		if err := store.save(&diagnosticReport{Schema: 1, JobID: id}); err != nil {
			t.Fatal(err)
		}
	}
	ids, _ := store.ids()
	if len(ids) != 10 || ids[0] != 3 || ids[9] != 12 {
		t.Fatalf("ids %v", ids)
	}
	// A new daemon instance (idle exit) continues after the newest report.
	fresh := &diagnosticStore{dir: dir, keep: 10}
	if id, _ := fresh.nextID(); id != 13 {
		t.Fatalf("next id %d", id)
	}
	if _, err := fresh.load(99, false); err == nil {
		t.Fatal("unknown report")
	}
	if _, err := (&diagnosticStore{dir: filepath.Join(dir, "absent")}).load(0, false); err == nil {
		t.Fatal("no reports yet")
	}
}

func TestListTestsReportsEveryContractTest(t *testing.T) {
	s := newDiagnosticsService(&Activity{last: time.Now()}, nil)
	s.newEnv = func(locale string, bootsBack int) *diagnostics.Env {
		return &diagnostics.Env{
			ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist },
			LookPath: func(name string) (string, error) {
				if name == "rpm" {
					return "/usr/bin/rpm", nil
				}
				return "", errors.New("absent")
			},
		}
	}
	tests, err := s.ListTests()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]DiagnosticTest{}
	for _, test := range tests {
		got[test.Id] = test
	}
	want := []string{"packages", "disk-health", "fs-scrub", "cpu-stress", "memory-stress", "disk-bench", "logs", "platform", "security", "network"}
	if len(got) != len(want) {
		t.Fatalf("tests %+v", tests)
	}
	for _, id := range want {
		if _, ok := got[id]; !ok {
			t.Errorf("missing %s", id)
		}
	}
	if !got["packages"].Available || got["disk-health"].Reason != "missing-tool:smartmontools" || !got["cpu-stress"].RequiresAc {
		t.Fatalf("rows %+v", got)
	}
}

func TestStartRefusesLoadTestsOnBatteryAndLowResources(t *testing.T) {
	s := fakeDiagnostics(t, map[string]diagnostics.TestFunc{"cpu-stress": okStage("cpu-stress"), "logs": okStage("logs")})
	kind := "on-battery"
	s.lookup = func(id string) (diagnostics.Test, bool) {
		return diagnostics.Test{ID: id, EstimatedSeconds: 1, Run: okStage(id), Preflight: func(*diagnostics.Env) error {
			if id == "logs" {
				return nil
			}
			return &diagnostics.PreflightError{Kind: kind, Detail: id}
		}}, id == "cpu-stress" || id == "logs"
	}
	if _, err := s.start([]string{"logs", "cpu-stress"}, nil); err == nil || err.Name != BusName+".Error.OnBattery" {
		t.Fatalf("battery: %v", err)
	}
	if release, busy := operations.beginTransaction(); busy != nil {
		t.Fatal("a refused start held the gate")
	} else {
		release()
	}
	kind = "insufficient-resources"
	if _, err := s.start([]string{"cpu-stress"}, nil); err == nil || err.Name != BusName+".Error.InsufficientResources" {
		t.Fatalf("resources: %v", err)
	}
	if id, err := s.start([]string{"logs"}, nil); err != nil || id == 0 {
		t.Fatalf("logs alone: %d %v", id, err)
	}
	waitIdle(t, s)
}

func TestSampleSignalCarriesOnlyMeasuredKeysWithContractTypes(t *testing.T) {
	pkg, mhz, load := 81, 3600, 11.5
	values := sampleVariants(diagnostics.Sample{PkgTempC: &pkg, AvgMHz: &mhz, Load1: &load})
	if len(values) != 3 {
		t.Fatalf("values %+v", values)
	}
	if values["pkgTempC"].Signature().String() != "i" || values["avgMHz"].Signature().String() != "u" || values["load1"].Signature().String() != "d" {
		t.Fatalf("signatures %+v", values)
	}
}

func TestStartPassesDurationAndMemoryOptionsToTheTests(t *testing.T) {
	got := make(chan [2]int, 1)
	s := fakeDiagnostics(t, map[string]diagnostics.TestFunc{
		"memory-stress": func(ctx context.Context, env *diagnostics.Env, progress diagnostics.Progress) diagnostics.StageResult {
			got <- [2]int{env.DurationSeconds, env.MemoryPercent}
			return diagnostics.StageResult{Verdict: diagnostics.Ok}
		},
	})
	_, err := s.start([]string{"memory-stress"}, map[string]dbus.Variant{"durationSeconds": dbus.MakeVariant(uint32(60)), "memoryPercent": dbus.MakeVariant(uint32(40))})
	if err != nil {
		t.Fatal(err)
	}
	if options := <-got; options != [2]int{60, 40} {
		t.Fatalf("options %v", options)
	}
	waitIdle(t, s)
}

// repairable stores job 7 with a changed binary and a failed unit, as the
// packages and logs tests would report them.
func repairable(t *testing.T, s *DiagnosticsService) {
	t.Helper()
	report := &diagnosticReport{Schema: 1, JobID: 7, Started: "now", Locale: "pt-BR", Host: map[string]string{}, Stages: []diagnostics.StageResult{
		{ID: "packages", Verdict: diagnostics.Attention, Findings: []diagnostics.Finding{
			{Severity: diagnostics.SeverityAttention, Code: "packages.binary-changed", Remedy: "reinstall-packages", RemedyArgs: []string{"vegad"}},
		}},
		{ID: "logs", Verdict: diagnostics.Attention, Findings: []diagnostics.Finding{
			{Severity: diagnostics.SeverityAttention, Code: "logs.failed-units", Remedy: "restart-units", RemedyArgs: []string{"x.service"}},
			{Severity: diagnostics.SeverityAttention, Code: "logs.coredumps"},
		}},
	}}
	if err := s.store.save(report); err != nil {
		t.Fatal(err)
	}
}

func TestRepairRefusesWhatTheReportDoesNotPropose(t *testing.T) {
	s := fakeDiagnostics(t, nil)
	repairable(t, s)
	for _, remedies := range [][]string{nil, {"format-disk"}, {"enable-ntp"}, {"restart-units", "restart-units"}} {
		if _, err := s.repair(7, remedies); err == nil || err.Name != BusName+".Error.NotSupported" {
			t.Errorf("%v: %v", remedies, err)
		}
	}
	if _, err := s.repair(99, []string{"restart-units"}); err == nil {
		t.Error("unknown job accepted")
	}
}

func TestRepairUsesStoredArgumentsSnapshotsAndRetests(t *testing.T) {
	s := fakeDiagnostics(t, map[string]diagnostics.TestFunc{"packages": okStage("packages"), "logs": okStage("logs")})
	repairable(t, s)
	var mu sync.Mutex
	var calls []string
	snapshotted := false
	s.snapshot = func(action string, work func() error) error {
		mu.Lock()
		snapshotted = strings.Contains(action, "7")
		mu.Unlock()
		return work()
	}
	base := s.newEnv
	s.newEnv = func(locale string, bootsBack int) *diagnostics.Env {
		env := base(locale, bootsBack)
		env.Run = func(ctx context.Context, name string, args ...string) ([]byte, int, error) {
			mu.Lock()
			calls = append(calls, strings.Join(append([]string{name}, args...), " "))
			mu.Unlock()
			return nil, 0, nil
		}
		return env
	}
	id, err := s.repair(7, []string{"restart-units", "reinstall-packages"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(calls, "\n")
	if !snapshotted || !strings.Contains(joined, "zypper -n --no-refresh install -f vegad") || !strings.Contains(joined, "systemctl restart -- x.service") {
		t.Fatalf("snapshot %v calls %v", snapshotted, calls)
	}
	report := loadReport(t, s, id, false)
	var ids []string
	for _, stage := range report["stages"].([]any) {
		ids = append(ids, stage.(map[string]any)["id"].(string))
	}
	// Packages before services (contract order), then the tests run again.
	if strings.Join(ids, ",") != "repair:reinstall-packages,repair:restart-units,packages,logs" || report["repairOf"] != float64(7) || report["locale"] != "pt-BR" {
		t.Fatalf("stages %v report %+v", ids, report)
	}
	if release, busy := operations.beginTransaction(); busy != nil {
		t.Fatal("gate held after the repair")
	} else {
		release()
	}
}

// twoPorts stores job 8 with two exposed ports, each its own finding, and a
// finding without remedy.
func twoPorts(t *testing.T, s *DiagnosticsService) {
	t.Helper()
	report := &diagnosticReport{Schema: 1, JobID: 8, Started: "now", Host: map[string]string{}, Stages: []diagnostics.StageResult{
		{ID: "security", Verdict: diagnostics.Attention, Findings: []diagnostics.Finding{
			{Severity: diagnostics.SeverityAttention, Code: "security.exposed-port", Remedy: "close-port", RemedyArgs: []string{"public|port:8000/tcp"}},
			{Severity: diagnostics.SeverityAttention, Code: "security.exposed-port", Remedy: "close-port", RemedyArgs: []string{"public|port:9000/tcp"}},
			{Severity: diagnostics.Info, Code: "security.secure-boot-off"},
		}},
		{ID: "logs", Verdict: diagnostics.Attention, Metrics: map[string]any{"examinedUntil": float64(1793316162142129)}, Findings: []diagnostics.Finding{
			{Severity: diagnostics.SeverityAttention, Code: "logs.coredumps"},
		}},
	}}
	if err := s.store.save(report); err != nil {
		t.Fatal(err)
	}
}

func TestRemediesListsFindingsWithAFixByIndex(t *testing.T) {
	s := fakeDiagnostics(t, nil)
	twoPorts(t, s)
	refs, err := s.Remedies(0)
	if err != nil {
		t.Fatal(err)
	}
	want := []RemedyRef{{"security", 0, "close-port"}, {"security", 1, "close-port"}}
	if len(refs) != 2 || refs[0] != want[0] || refs[1] != want[1] {
		t.Fatalf("%+v", refs)
	}
}

func TestRepairFindingsFixesOnlyTheChosenPort(t *testing.T) {
	s := fakeDiagnostics(t, map[string]diagnostics.TestFunc{"security": okStage("security")})
	twoPorts(t, s)
	var mu sync.Mutex
	var calls []string
	s.snapshot = func(_ string, work func() error) error { return work() }
	base := s.newEnv
	s.newEnv = func(locale string, bootsBack int) *diagnostics.Env {
		env := base(locale, bootsBack)
		env.Run = func(ctx context.Context, name string, args ...string) ([]byte, int, error) {
			mu.Lock()
			calls = append(calls, strings.Join(append([]string{name}, args...), " "))
			mu.Unlock()
			return nil, 0, nil
		}
		return env
	}
	if _, err := s.repairFindings(8, []FindingRef{{"security", 0}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "8000/tcp") || strings.Contains(joined, "9000/tcp") {
		t.Fatalf("calls %v", calls)
	}
}

func TestRepairFindingsRefusesBadReferences(t *testing.T) {
	s := fakeDiagnostics(t, nil)
	twoPorts(t, s)
	for _, refs := range [][]FindingRef{nil, {{"security", 99}}, {{"security", 2}}, {{"disk", 0}}, {{"security", 0}, {"security", 0}}} {
		if _, err := s.repairFindings(8, refs); err == nil || err.Name != BusName+".Error.NotSupported" {
			t.Errorf("%v: %v", refs, err)
		}
	}
}

func TestMarkLogsSeenStoresTheCutoffAndNeverMovesItBack(t *testing.T) {
	s := fakeDiagnostics(t, nil)
	twoPorts(t, s)
	if err := s.markLogsSeen(8); err != nil {
		t.Fatal(err)
	}
	if got := s.seen.load().UnixMicro(); got != 1793316162142129 {
		t.Fatalf("cutoff %d", got)
	}
	if err := s.seen.save(time.UnixMicro(1)); err != nil || s.seen.load().UnixMicro() != 1793316162142129 {
		t.Fatalf("cutoff moved back: %v", err)
	}
	repairable(t, s) // job 7: logs stage without examinedUntil
	if err := s.markLogsSeen(7); err == nil || err.Name != BusName+".Error.NotSupported" {
		t.Fatalf("old report accepted: %v", err)
	}
	if ids, _ := s.store.ids(); len(ids) != 2 {
		t.Fatalf("the cutoff file must not look like a report: %v", ids)
	}
}

func TestNetworkTestAloneUsesTheSilentAction(t *testing.T) {
	cases := map[string]struct {
		stages []string
		action string
	}{
		"network alone":     {[]string{"network"}, networkDiagnoseAction},
		"network repair":    {[]string{"repair:temporary-dns", "network"}, networkDiagnoseAction},
		"with another test": {[]string{"network", "logs"}, diagnosticsAction},
		"another repair":    {[]string{"repair:close-port", "network"}, diagnosticsAction},
		"nothing (no job)":  {nil, diagnosticsAction},
	}
	for name, tc := range cases {
		if got := actionFor(tc.stages); got != tc.action {
			t.Errorf("%s: %s", name, got)
		}
	}
}
