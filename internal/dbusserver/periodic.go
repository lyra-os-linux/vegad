package dbusserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/diagnostics"
	vegai18n "github.com/lyraos/vegad/internal/i18n"
)

// The weekly check (add-periodic-checks): vegad-health-check.timer runs the
// light tests through Start with the "periodic" option, and the result is
// compared with the previous run. Only what is new reaches the user.

// periodicTests are read-only and cause no load: no stress, scrub or bench.
var periodicTests = []string{"disk-health", "logs", "platform", "security"}

const (
	periodicTimer = "vegad-health-check.timer"
	// A finding that stays is not news; these thresholds are.
	wearRiseLimit   = 5.0
	wearWindow      = 30 * 24 * time.Hour
	patchesOverdue  = 7 * 24 * time.Hour
	lowBatteryLimit = 30
	patchesCode     = "security.patches-pending"
)

var periodicDropIn = "/etc/systemd/system/" + periodicTimer + ".d/10-vega-frequency.conf"

// wearPoint is one SMART percentUsed reading of one disk.
type wearPoint struct {
	At      int64   `json:"at"`
	Percent float64 `json:"percent"`
}

// periodicState is /var/lib/vega/diagnostics/periodic.json.
type periodicState struct {
	JobID    uint32 `json:"jobId"`
	Finished int64  `json:"finished"`
	// Seen maps each attention/fail finding key to when it first appeared.
	Seen map[string]int64 `json:"seen"`
	// PatchesNotified avoids repeating the overdue patches notice.
	PatchesNotified bool `json:"patchesNotified,omitempty"`
	// Wear keeps 60 days of percentUsed per disk; WearNotified the last
	// notice, so a rise is announced once a month at most.
	Wear         map[string][]wearPoint `json:"wear"`
	WearNotified int64                  `json:"wearNotified,omitempty"`
	Stages       [][3]string            `json:"stages"`
	Novelties    []string               `json:"novelties"`
}

func periodicStatePath() string {
	return filepath.Join(diagnosticsReportDir, "periodic.json")
}

func loadPeriodicState(path string) periodicState {
	state := periodicState{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	if state.Seen == nil {
		state.Seen = map[string]int64{}
	}
	if state.Wear == nil {
		state.Wear = map[string][]wearPoint{}
	}
	return state
}

func savePeriodicState(path string, state periodicState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeConfigAtomicallyWithMode(path, data, 0o600)
}

// findingKey tells findings apart: the code plus the remedy arguments, so a
// second exposed port is new even when another one was already known.
func findingKey(f diagnostics.Finding) string {
	return f.Code + "|" + strings.Join(f.RemedyArgs, ",")
}

func number(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint8:
		return float64(v), true
	}
	return 0, false
}

// comparePeriodic returns the next state and the texts of what is new.
func comparePeriodic(prev periodicState, report *diagnosticReport, now time.Time) (periodicState, []string) {
	next := periodicState{
		JobID: report.JobID, Finished: now.Unix(),
		Seen: map[string]int64{}, Wear: map[string][]wearPoint{},
		PatchesNotified: prev.PatchesNotified, WearNotified: prev.WearNotified,
	}
	var novelties []string
	patchesPresent := false
	for _, stage := range report.Stages {
		next.Stages = append(next.Stages, [3]string{stage.ID, string(stage.Verdict), stage.Summary})
		for _, finding := range stage.Findings {
			if finding.Severity != diagnostics.SeverityAttention && finding.Severity != diagnostics.SeverityFail {
				continue
			}
			key := findingKey(finding)
			first, known := prev.Seen[key]
			if !known {
				first = now.Unix()
				// Patches are news only after a week (below), not on arrival.
				if finding.Code != patchesCode {
					novelties = append(novelties, finding.Text)
				}
			}
			next.Seen[key] = first
			if finding.Code == patchesCode {
				patchesPresent = true
				if !next.PatchesNotified && now.Sub(time.Unix(first, 0)) > patchesOverdue {
					novelties = append(novelties, finding.Text)
					next.PatchesNotified = true
				}
			}
		}
		if stage.ID != "disk-health" {
			continue
		}
		for disk, metrics := range stage.Metrics {
			values, ok := metrics.(map[string]any)
			if !ok {
				continue
			}
			percent, ok := number(values["percentUsed"])
			if !ok {
				continue
			}
			history := []wearPoint{}
			for _, point := range prev.Wear[disk] {
				if now.Sub(time.Unix(point.At, 0)) <= 2*wearWindow {
					history = append(history, point)
				}
			}
			for _, point := range history {
				recent := now.Sub(time.Unix(point.At, 0)) <= wearWindow
				quiet := next.WearNotified == 0 || now.Sub(time.Unix(next.WearNotified, 0)) > wearWindow
				if recent && quiet && percent-point.Percent > wearRiseLimit {
					text := vegai18n.T(report.Locale, "diagnostics.periodic.wear")
					text = strings.NewReplacer("{disk}", disk, "{from}", fmt.Sprintf("%.0f", point.Percent), "{to}", fmt.Sprintf("%.0f", percent)).Replace(text)
					novelties = append(novelties, text)
					next.WearNotified = now.Unix()
					break
				}
			}
			next.Wear[disk] = append(history, wearPoint{At: now.Unix(), Percent: percent})
		}
	}
	if !patchesPresent {
		next.PatchesNotified = false
	}
	next.Novelties = novelties
	return next, novelties
}

// afterPeriodic compares the finished run, keeps the state and announces
// what is new.
func (s *DiagnosticsService) afterPeriodic(job *runningDiagnostic, report *diagnosticReport) {
	path := s.periodicPath
	next, novelties := comparePeriodic(loadPeriodicState(path), report, time.Now())
	if err := savePeriodicState(path, next); err != nil {
		log.Printf("vegad: estado da verificação periódica: %v", err)
	}
	if len(novelties) > 0 {
		s.emit("Novelties", job.id, uint32(len(novelties)))
	}
}

// PeriodicStage is the (sss) element of LastPeriodic.
type PeriodicStage struct {
	Id      string
	Verdict string
	Summary string
}

// LastPeriodic is a silent read (hardware.read-admin, checked by the router).
func (s *DiagnosticsService) LastPeriodic() (uint32, int64, uint32, []PeriodicStage, []string, *dbus.Error) {
	s.activity.Touch()
	state := loadPeriodicState(s.periodicPath)
	stages := []PeriodicStage{}
	for _, stage := range state.Stages {
		stages = append(stages, PeriodicStage{stage[0], stage[1], stage[2]})
	}
	texts := state.Novelties
	if texts == nil {
		texts = []string{}
	}
	return state.JobID, state.Finished, uint32(len(texts)), stages, texts, nil
}

// systemctlRun runs systemctl; replaced in tests.
var systemctlRun = func(args ...string) ([]byte, error) {
	return exec.Command("systemctl", args...).CombinedOutput()
}

func (s *DiagnosticsService) GetPeriodic() (bool, string, *dbus.Error) {
	s.activity.Touch()
	enabled, frequency := periodicSettings()
	return enabled, frequency, nil
}

func periodicSettings() (bool, string) {
	out, _ := systemctlRun("is-enabled", periodicTimer)
	enabled := strings.TrimSpace(string(out)) == "enabled"
	frequency := "weekly"
	if data, err := os.ReadFile(periodicDropIn); err == nil && strings.Contains(string(data), "OnCalendar=daily") {
		frequency = "daily"
	}
	return enabled, frequency
}

func (s *DiagnosticsService) SetPeriodic(sender dbus.Sender, enabled bool, frequency string) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, diagnosticsAction); err != nil {
		return err
	}
	if err := setPeriodic(enabled, frequency); err != nil {
		var unsupported *dbus.Error
		if errors.As(err, &unsupported) {
			return unsupported
		}
		return dbus.MakeFailedError(err)
	}
	return nil
}

func setPeriodic(enabled bool, frequency string) error {
	switch frequency {
	case "weekly":
		if err := os.Remove(periodicDropIn); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	case "daily":
		if err := os.MkdirAll(filepath.Dir(periodicDropIn), 0o755); err != nil {
			return err
		}
		// The empty OnCalendar= resets the packaged weekly schedule.
		if err := writeConfigAtomicallyWithMode(periodicDropIn, []byte("[Timer]\nOnCalendar=\nOnCalendar=daily\n"), 0o644); err != nil {
			return err
		}
	default:
		return notSupported("frequência desconhecida: " + frequency)
	}
	if out, err := systemctlRun("daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w: %s", err, out)
	}
	verb := "disable"
	if enabled {
		verb = "enable"
	}
	if out, err := systemctlRun(verb, "--now", periodicTimer); err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", verb, err, out)
	}
	return nil
}

// powerSupplyDir is /sys/class/power_supply; replaced in tests.
var powerSupplyDir = "/sys/class/power_supply"

// lowBattery: running on a battery under 30% with no charger online.
func lowBattery() bool {
	entries, err := os.ReadDir(powerSupplyDir)
	if err != nil {
		return false
	}
	read := func(name, file string) string {
		data, _ := os.ReadFile(filepath.Join(powerSupplyDir, name, file))
		return strings.TrimSpace(string(data))
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	low := false
	for _, name := range names {
		switch read(name, "type") {
		case "Mains", "USB":
			if read(name, "online") == "1" {
				return false
			}
		case "Battery":
			if capacity, err := strconv.Atoi(read(name, "capacity")); err == nil && capacity < lowBatteryLimit {
				low = true
			}
		}
	}
	return low
}

// lowPriority runs every tool of a periodic job under nice and idle I/O, so
// the check never competes with what the user is doing.
func lowPriority(run diagnostics.Runner) diagnostics.Runner {
	return func(ctx context.Context, name string, args ...string) ([]byte, int, error) {
		return run(ctx, "nice", append([]string{"-n", "19", "ionice", "-c3", name}, args...)...)
	}
}

// RunHealthCheck is vegad-health-check.service: it asks the (bus-activated)
// daemon to run the weekly check as root and returns at once; the daemon owns
// the job, the exclusivity gate and the comparison.
func RunHealthCheck() error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	options := map[string]dbus.Variant{
		"periodic": dbus.MakeVariant(true),
		"locale":   dbus.MakeVariant(systemLocale()),
	}
	var job uint32
	call := conn.Object(BusName, ObjectPath).Call(BusName+".Diagnostics.Start", 0, periodicTests, options)
	if err := call.Store(&job); err != nil {
		return err
	}
	if job == 0 {
		log.Printf("vegad: verificação periódica adiada")
	} else {
		log.Printf("vegad: verificação periódica iniciada (job %d)", job)
	}
	return nil
}

// systemLocale is LANG from /etc/locale.conf: the texts of the weekly check
// follow the system language.
func systemLocale() string {
	data, err := os.ReadFile("/etc/locale.conf")
	if err != nil {
		return vegai18n.DefaultLocale
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "LANG="); ok {
			return vegai18n.Normalize(strings.Trim(value, `"'`))
		}
	}
	return vegai18n.DefaultLocale
}
