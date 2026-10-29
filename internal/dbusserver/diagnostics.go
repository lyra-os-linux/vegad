package dbusserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/diagnostics"
	vegai18n "github.com/lyraos/vegad/internal/i18n"
	"github.com/lyraos/vegad/internal/version"
)

// DiagnosticTest is the (sbsub) row of Diagnostics.ListTests.
type DiagnosticTest struct {
	Id               string
	Available        bool
	Reason           string
	EstimatedSeconds uint32
	RequiresAc       bool
}

// DiagnosticJob is the (uassu) reply of Diagnostics.Current.
type DiagnosticJob struct {
	JobId   uint32
	Tests   []string
	Stage   string
	Percent uint32
}

const diagnosticsAction = "org.lyraos.vega.hardware.diagnose"

// DiagnosticsService implements org.lyraos.Vega1.Diagnostics (vegad#98): one
// job at a time, exclusive with system transactions (operationGate), with
// progress signals and a JSON report kept on disk.
type DiagnosticsService struct {
	activity *Activity
	conn     *dbus.Conn
	store    *diagnosticStore
	// newEnv builds the host environment for a job; replaced in tests.
	newEnv func(locale string, bootsBack int) *diagnostics.Env
	// lookup resolves a test id; replaced in tests.
	lookup func(id string) (diagnostics.Test, bool)
	// profile tunes the security test (SSH and 9090 expected on servers).
	profile string
	// snapshot wraps a repair in a pre/post snapshot; replaced in tests.
	snapshot func(action string, work func() error) error

	mu  sync.Mutex
	job *runningDiagnostic
}

type runningDiagnostic struct {
	id      uint32
	tests   []string
	stage   string
	percent uint32
	cancel  context.CancelFunc
}

func newDiagnosticsService(activity *Activity, conn *dbus.Conn) *DiagnosticsService {
	return &DiagnosticsService{
		activity: activity,
		conn:     conn,
		store:    &diagnosticStore{dir: diagnosticsReportDir, keep: 10},
		newEnv:   diagnostics.HostEnv,
		lookup:   diagnostics.Lookup,
		snapshot: withSnapshots,
	}
}

func notSupported(detail string) *dbus.Error {
	return dbus.NewError(BusName+".Error.NotSupported", []interface{}{detail})
}

func (s *DiagnosticsService) ListTests() ([]DiagnosticTest, *dbus.Error) {
	s.activity.Touch()
	env := s.newEnv(vegai18n.DefaultLocale, 8)
	tests := make([]DiagnosticTest, 0, len(diagnostics.Tests))
	for _, test := range diagnostics.Tests {
		available, reason := test.Availability(env)
		tests = append(tests, DiagnosticTest{
			Id: test.ID, Available: available, Reason: reason,
			EstimatedSeconds: test.Seconds(env), RequiresAc: test.RequiresAC,
		})
	}
	return tests, nil
}

func (s *DiagnosticsService) Current() (DiagnosticJob, *dbus.Error) {
	s.activity.Touch()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job == nil {
		return DiagnosticJob{Tests: []string{}}, nil
	}
	return DiagnosticJob{JobId: s.job.id, Tests: append([]string{}, s.job.tests...), Stage: s.job.stage, Percent: s.job.percent}, nil
}

type diagnosticOptions struct {
	durationSeconds uint32
	memoryPercent   uint32
	bootsBack       uint32
	locale          string
}

func parseDiagnosticOptions(options map[string]dbus.Variant) (diagnosticOptions, *dbus.Error) {
	parsed := diagnosticOptions{durationSeconds: 240, memoryPercent: 70, bootsBack: 8, locale: vegai18n.DefaultLocale}
	ranged := func(key string, value dbus.Variant, min, max uint32, target *uint32) *dbus.Error {
		number, ok := value.Value().(uint32)
		if !ok || number < min || number > max {
			return notSupported(fmt.Sprintf("%s deve ser u entre %d e %d", key, min, max))
		}
		*target = number
		return nil
	}
	for key, value := range options {
		var err *dbus.Error
		switch key {
		case "durationSeconds":
			err = ranged(key, value, 30, 600, &parsed.durationSeconds)
		case "memoryPercent":
			err = ranged(key, value, 10, 70, &parsed.memoryPercent)
		case "bootsBack":
			err = ranged(key, value, 1, 20, &parsed.bootsBack)
		case "locale":
			locale, ok := value.Value().(string)
			if !ok {
				return parsed, notSupported("locale deve ser s")
			}
			parsed.locale = vegai18n.Normalize(locale)
		}
		if err != nil {
			return parsed, err
		}
	}
	return parsed, nil
}

func (s *DiagnosticsService) Start(sender dbus.Sender, tests []string, options map[string]dbus.Variant) (uint32, *dbus.Error) {
	s.activity.Touch()
	if err := requirePolkit(sender, diagnosticsAction); err != nil {
		return 0, err
	}
	return s.start(tests, options)
}

func (s *DiagnosticsService) start(tests []string, options map[string]dbus.Variant) (uint32, *dbus.Error) {
	if len(tests) == 0 {
		return 0, notSupported("nenhum teste pedido")
	}
	seen := map[string]bool{}
	for _, id := range tests {
		if _, ok := s.lookup(id); !ok {
			return 0, notSupported("teste desconhecido: " + id)
		}
		if seen[id] {
			return 0, notSupported("teste repetido: " + id)
		}
		seen[id] = true
	}
	opts, err := parseDiagnosticOptions(options)
	if err != nil {
		return 0, err
	}
	env := s.newEnv(opts.locale, int(opts.bootsBack))
	if s.profile != "" {
		env.Profile = s.profile
	}
	env.DurationSeconds = int(opts.durationSeconds)
	env.MemoryPercent = int(opts.memoryPercent)
	// Safeguards that refuse the whole job up front; a test that is merely
	// unavailable still runs as an "unavailable" stage.
	for _, id := range tests {
		test, _ := s.lookup(id)
		if available, _ := test.Availability(env); !available {
			continue
		}
		if err := test.Check(env); err != nil {
			return 0, preflightError(err)
		}
	}
	release, busy := operations.beginDiagnostic()
	if busy != nil {
		return 0, busy
	}
	done, ok := s.activity.begin()
	if !ok {
		release()
		return 0, shuttingDownError()
	}
	id, storeErr := s.store.nextID()
	if storeErr != nil {
		done()
		release()
		return 0, dbus.MakeFailedError(storeErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &runningDiagnostic{id: id, tests: append([]string{}, tests...), stage: tests[0], cancel: cancel}
	s.mu.Lock()
	s.job = job
	s.mu.Unlock()
	env.Sample = func(sample diagnostics.Sample) { s.emit("Sample", id, sampleVariants(sample)) }
	go func() {
		defer done()
		defer release()
		defer cancel()
		s.run(ctx, job, env)
	}()
	return id, nil
}

func (s *DiagnosticsService) Cancel(sender dbus.Sender, jobID uint32) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, diagnosticsAction); err != nil {
		return err
	}
	s.cancel(jobID)
	return nil
}

func (s *DiagnosticsService) cancel(jobID uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job != nil && s.job.id == jobID {
		s.job.cancel()
	}
}

func (s *DiagnosticsService) Report(sender dbus.Sender, jobID uint32, redact bool) (string, *dbus.Error) {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.logs.read-admin"); err != nil {
		return "", err
	}
	return s.report(jobID, redact)
}

func (s *DiagnosticsService) report(jobID uint32, redact bool) (string, *dbus.Error) {
	report, err := s.store.load(jobID, redact)
	if err != nil {
		return "", dbus.MakeFailedError(err)
	}
	return report, nil
}

const repairAction = "org.lyraos.vega.hardware.repair"

func (s *DiagnosticsService) Repair(sender dbus.Sender, jobID uint32, remedies []string) (uint32, *dbus.Error) {
	s.activity.Touch()
	if err := requirePolkit(sender, repairAction); err != nil {
		return 0, err
	}
	return s.repair(jobID, remedies)
}

type plannedRemedy struct {
	remedy diagnostics.Remedy
	args   []string
}

// repair plans the requested remedies from the stored report of jobID. The
// arguments (packages, units) come only from that report.
func (s *DiagnosticsService) repair(jobID uint32, remedies []string) (uint32, *dbus.Error) {
	if len(remedies) == 0 {
		return 0, notSupported("nenhum reparo pedido")
	}
	requested := map[string]bool{}
	for _, id := range remedies {
		if _, ok := diagnostics.LookupRemedy(id); !ok {
			return 0, notSupported("reparo desconhecido: " + id)
		}
		if requested[id] {
			return 0, notSupported("reparo repetido: " + id)
		}
		requested[id] = true
	}
	source, err := s.store.loadReport(jobID)
	if err != nil {
		return 0, notSupported(err.Error())
	}
	var plan []plannedRemedy
	var retests []string
	for _, remedy := range diagnostics.Remedies {
		if !requested[remedy.ID] {
			continue
		}
		found := false
		seen := map[string]bool{}
		var args []string
		for _, stage := range source.Stages {
			for _, finding := range stage.Findings {
				if finding.Remedy != remedy.ID {
					continue
				}
				found = true
				for _, arg := range finding.RemedyArgs {
					if !seen[arg] {
						seen[arg] = true
						args = append(args, arg)
					}
				}
			}
		}
		if !found {
			return 0, notSupported(fmt.Sprintf("o relatório %d não propõe %s", source.JobID, remedy.ID))
		}
		plan = append(plan, plannedRemedy{remedy, args})
		if !slices.Contains(retests, remedy.Retest) {
			retests = append(retests, remedy.Retest)
		}
	}
	release, busy := operations.beginDiagnostic()
	if busy != nil {
		return 0, busy
	}
	done, ok := s.activity.begin()
	if !ok {
		release()
		return 0, shuttingDownError()
	}
	id, storeErr := s.store.nextID()
	if storeErr != nil {
		done()
		release()
		return 0, dbus.MakeFailedError(storeErr)
	}
	stages := make([]string, 0, len(plan)+len(retests))
	for _, p := range plan {
		stages = append(stages, "repair:"+p.remedy.ID)
	}
	stages = append(stages, retests...)
	ctx, cancel := context.WithCancel(context.Background())
	job := &runningDiagnostic{id: id, tests: stages, stage: stages[0], cancel: cancel}
	s.mu.Lock()
	s.job = job
	s.mu.Unlock()
	locale := source.Locale
	if locale == "" {
		locale = vegai18n.DefaultLocale
	}
	env := s.newEnv(locale, 8)
	if s.profile != "" {
		env.Profile = s.profile
	}
	go func() {
		defer done()
		defer release()
		defer cancel()
		s.runRepair(ctx, job, env, source.JobID, plan, retests)
	}()
	return id, nil
}

func (s *DiagnosticsService) runRepair(ctx context.Context, job *runningDiagnostic, env *diagnostics.Env, of uint32, plan []plannedRemedy, retests []string) {
	inhibitor, err := acquireShutdownInhibit("Reparo do diagnóstico")
	if err != nil {
		log.Printf("vegad: reparo %d sem inibição de desligamento: %v", job.id, err)
	}
	defer inhibitor.Release()
	report := s.newReport(job, env)
	report.RepairOf = of
	s.save(report)
	_ = s.snapshot(fmt.Sprintf("Vega: reparo do diagnóstico %d", of), func() error {
		for _, p := range plan {
			p := p
			s.stage(ctx, job, env, report, "repair:"+p.remedy.ID, 15*time.Minute, true, "", func(ctx context.Context, progress diagnostics.Progress) diagnostics.StageResult {
				return p.remedy.Run(ctx, env, p.args, progress)
			})
		}
		return nil
	})
	// The tests run again to say whether the problems are gone.
	for _, id := range retests {
		s.testStage(ctx, job, env, report, id)
	}
	s.finish(job, env, report)
}

func (s *DiagnosticsService) setProgress(job *runningDiagnostic, stage string, percent uint32, message string) {
	s.mu.Lock()
	job.stage, job.percent = stage, percent
	s.mu.Unlock()
	s.emit("Progress", job.id, stage, percent, message)
}

func (s *DiagnosticsService) emit(signal string, args ...interface{}) {
	if s.conn == nil {
		return
	}
	if err := s.conn.Emit(ObjectPath, BusName+".Diagnostics."+signal, args...); err != nil {
		log.Printf("vegad: emit Diagnostics.%s: %v", signal, err)
	}
}

func preflightError(err error) *dbus.Error {
	var preflight *diagnostics.PreflightError
	if errors.As(err, &preflight) {
		switch preflight.Kind {
		case "on-battery":
			return dbus.NewError(BusName+".Error.OnBattery", []interface{}{"conecte o computador à tomada: " + preflight.Detail})
		case "insufficient-resources":
			return dbus.NewError(BusName+".Error.InsufficientResources", []interface{}{preflight.Detail})
		}
	}
	return dbus.MakeFailedError(err)
}

// sampleVariants is the a{sv} of the Sample signal: only measured keys.
func sampleVariants(sample diagnostics.Sample) map[string]dbus.Variant {
	values := map[string]dbus.Variant{}
	add := func(key string, value *int) {
		if value != nil {
			if key == "avgMHz" || key == "throttleEvents" {
				values[key] = dbus.MakeVariant(uint32(max(0, *value)))
			} else {
				values[key] = dbus.MakeVariant(int32(*value))
			}
		}
	}
	add("pkgTempC", sample.PkgTempC)
	add("maxCoreTempC", sample.MaxCoreTempC)
	add("avgMHz", sample.AvgMHz)
	add("throttleEvents", sample.ThrottleEvents)
	add("nvmeTempC", sample.NvmeTempC)
	if sample.Load1 != nil {
		values["load1"] = dbus.MakeVariant(*sample.Load1)
	}
	return values
}

// stageTimeout is twice the estimate for this host, never under a minute.
func stageTimeout(test diagnostics.Test, env *diagnostics.Env) time.Duration {
	timeout := 2 * time.Duration(test.Seconds(env)) * time.Second
	if timeout < time.Minute {
		timeout = time.Minute
	}
	return timeout
}

func (s *DiagnosticsService) newReport(job *runningDiagnostic, env *diagnostics.Env) *diagnosticReport {
	report := &diagnosticReport{Schema: 1, JobID: job.id, Started: time.Now().Format(time.RFC3339), Host: hostInfo(env), Stages: []diagnostics.StageResult{}, Locale: env.Locale}
	if name, err := os.Hostname(); err == nil && name != "" {
		report.Host["hostname"] = name
		report.Sensitive = append(report.Sensitive, name)
	}
	return report
}

// stage runs one step of a job (a test or a repair) with its timeout,
// records it in the report and emits StageFinished.
func (s *DiagnosticsService) stage(ctx context.Context, job *runningDiagnostic, env *diagnostics.Env, report *diagnosticReport, id string, timeout time.Duration, available bool, reason string, run func(context.Context, diagnostics.Progress) diagnostics.StageResult) {
	s.setProgress(job, id, 0, "")
	var result diagnostics.StageResult
	switch {
	case ctx.Err() != nil:
		result = diagnostics.StageResult{ID: id, Verdict: diagnostics.Cancelled, Summary: vegai18n.T(env.Locale, "diagnostics.summary.not-run"), Findings: []diagnostics.Finding{}}
	case !available:
		result = diagnostics.StageResult{ID: id, Verdict: diagnostics.Unavailable, Summary: vegai18n.T(env.Locale, "diagnostics.summary.unavailable"), Findings: []diagnostics.Finding{}, Metrics: map[string]any{"reason": reason}}
	default:
		stageCtx, stop := context.WithTimeout(ctx, timeout)
		result = run(stageCtx, func(percent uint32, message string) {
			s.setProgress(job, id, percent, message)
		})
		stop()
		if ctx.Err() != nil && result.Verdict != diagnostics.Cancelled {
			result.Verdict = diagnostics.Cancelled
		}
	}
	result.ID = id
	if result.Findings == nil {
		result.Findings = []diagnostics.Finding{}
	}
	report.Sensitive = append(report.Sensitive, result.Sensitive...)
	report.Stages = append(report.Stages, result)
	s.setProgress(job, id, 100, result.Summary)
	s.emit("StageFinished", job.id, id, string(result.Verdict), result.Summary)
	s.save(report)
}

// testStage runs a health test as a stage of job.
func (s *DiagnosticsService) testStage(ctx context.Context, job *runningDiagnostic, env *diagnostics.Env, report *diagnosticReport, id string) {
	test, _ := s.lookup(id)
	available, reason := test.Availability(env)
	s.stage(ctx, job, env, report, id, stageTimeout(test, env), available, reason, func(ctx context.Context, progress diagnostics.Progress) diagnostics.StageResult {
		return test.Run(ctx, env, progress)
	})
}

func (s *DiagnosticsService) finish(job *runningDiagnostic, env *diagnostics.Env, report *diagnosticReport) {
	result := diagnostics.Aggregate(report.Stages)
	report.Result = string(result)
	report.Finished = time.Now().Format(time.RFC3339)
	s.save(report)
	s.mu.Lock()
	s.job = nil
	s.mu.Unlock()
	s.emit("Finished", job.id, string(result), vegai18n.T(env.Locale, "diagnostics.result."+string(result)))
}

func (s *DiagnosticsService) run(ctx context.Context, job *runningDiagnostic, env *diagnostics.Env) {
	inhibitor, err := acquireShutdownInhibit("Diagnóstico de saúde")
	if err != nil {
		log.Printf("vegad: diagnóstico %d sem inibição de desligamento: %v", job.id, err)
	}
	defer inhibitor.Release()
	report := s.newReport(job, env)
	s.save(report)
	for _, id := range job.tests {
		s.testStage(ctx, job, env, report, id)
	}
	s.finish(job, env, report)
}

func (s *DiagnosticsService) save(report *diagnosticReport) {
	if err := s.store.save(report); err != nil {
		log.Printf("vegad: salvar relatório do diagnóstico %d: %v", report.JobID, err)
	}
}

// hostInfo identifies the machine in the report. Product and firmware come
// from DMI; nothing here is personal except the hostname, kept separately.
func hostInfo(env *diagnostics.Env) map[string]string {
	read := func(path string) string {
		data, err := env.ReadFile(path)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
	osName := ""
	for _, line := range strings.Split(read("/etc/os-release"), "\n") {
		if value, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			osName = strings.Trim(value, `"`)
		}
	}
	return map[string]string{
		"product": read("/sys/class/dmi/id/product_name"),
		"bios":    read("/sys/class/dmi/id/bios_version"),
		"kernel":  read("/proc/sys/kernel/osrelease"),
		"os":      osName,
		"vegad":   version.Version,
	}
}

// diagnosticReport is the JSON report, schema 1 (lyra-vega-dbus
// docs/diagnostics.md). Result and Finished are absent while the job runs.
type diagnosticReport struct {
	Schema    int                       `json:"schema"`
	JobID     uint32                    `json:"jobId"`
	Started   string                    `json:"started"`
	Finished  string                    `json:"finished,omitempty"`
	Host      map[string]string         `json:"host"`
	Result    string                    `json:"result,omitempty"`
	Stages    []diagnostics.StageResult `json:"stages"`
	Sensitive []string                  `json:"sensitive,omitempty"`
	// Locale of the texts, reused by a repair of this job.
	Locale string `json:"locale,omitempty"`
	// RepairOf is the job a repair job fixed.
	RepairOf uint32 `json:"repairOf,omitempty"`
}

var diagnosticsReportDir = "/var/lib/vega/diagnostics"

// diagnosticStore keeps the last reports as <jobId>.json (0600). Job ids
// continue from the newest stored report, because the daemon exits when idle.
type diagnosticStore struct {
	dir    string
	keep   int
	mu     sync.Mutex
	lastID uint32
}

func (st *diagnosticStore) ids() ([]uint32, error) {
	entries, err := os.ReadDir(st.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []uint32
	for _, entry := range entries {
		name, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok {
			continue
		}
		if id, err := strconv.ParseUint(name, 10, 32); err == nil && id > 0 {
			ids = append(ids, uint32(id))
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

func (st *diagnosticStore) nextID() (uint32, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	ids, err := st.ids()
	if err != nil {
		return 0, err
	}
	if n := len(ids); n > 0 && ids[n-1] > st.lastID {
		st.lastID = ids[n-1]
	}
	st.lastID++
	return st.lastID, nil
}

func (st *diagnosticStore) path(id uint32) string {
	return filepath.Join(st.dir, strconv.FormatUint(uint64(id), 10)+".json")
}

func (st *diagnosticStore) save(report *diagnosticReport) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := os.MkdirAll(st.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(st.dir, ".report-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), st.path(report.JobID)); err != nil {
		return err
	}
	ids, err := st.ids()
	if err != nil {
		return err
	}
	for len(ids) > st.keep {
		_ = os.Remove(st.path(ids[0]))
		ids = ids[1:]
	}
	return nil
}

// loadReport reads the stored report of jobID (the newest when 0), whole.
func (st *diagnosticStore) loadReport(jobID uint32) (*diagnosticReport, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if jobID == 0 {
		ids, err := st.ids()
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("nenhum diagnóstico foi executado")
		}
		jobID = ids[len(ids)-1]
	}
	data, err := os.ReadFile(st.path(jobID))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("relatório %d não encontrado", jobID)
	}
	if err != nil {
		return nil, err
	}
	var report diagnosticReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, err
	}
	return &report, nil
}

// load returns the report of jobID (the newest when 0), without the
// sensitive list and, with redact, with those values masked.
func (st *diagnosticStore) load(jobID uint32, redact bool) (string, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if jobID == 0 {
		ids, err := st.ids()
		if err != nil {
			return "", err
		}
		if len(ids) == 0 {
			return "", fmt.Errorf("nenhum diagnóstico foi executado")
		}
		jobID = ids[len(ids)-1]
	}
	data, err := os.ReadFile(st.path(jobID))
	if os.IsNotExist(err) {
		return "", fmt.Errorf("relatório %d não encontrado", jobID)
	}
	if err != nil {
		return "", err
	}
	var report map[string]any
	if err := json.Unmarshal(data, &report); err != nil {
		return "", err
	}
	var sensitive []string
	if list, ok := report["sensitive"].([]any); ok {
		for _, value := range list {
			if text, ok := value.(string); ok && text != "" {
				sensitive = append(sensitive, text)
			}
		}
	}
	delete(report, "sensitive")
	if redact {
		report = redactValue(report, sensitive).(map[string]any)
	}
	out, err := json.Marshal(report)
	return string(out), err
}

var (
	macPattern  = regexp.MustCompile(`(?i)\b([0-9a-f]{2}[:-]){5}[0-9a-f]{2}\b`)
	ssidPattern = regexp.MustCompile(`(?i)(ssid[ =:]+['"]?)([^'"\s,;]+)`)
	homePattern = regexp.MustCompile(`/home/[^/\s'"]+`)
)

const redacted = "[redacted]"

// redactValue masks the job's sensitive values (serial numbers, hostname)
// and any MAC address, SSID or home directory name wherever they appear,
// including log lines and crash paths.
func redactValue(value any, sensitive []string) any {
	switch v := value.(type) {
	case string:
		for _, secret := range sensitive {
			v = strings.ReplaceAll(v, secret, redacted)
		}
		v = macPattern.ReplaceAllString(v, redacted)
		v = homePattern.ReplaceAllString(v, "/home/"+redacted)
		return ssidPattern.ReplaceAllString(v, "${1}"+redacted)
	case []any:
		for i := range v {
			v[i] = redactValue(v[i], sensitive)
		}
		return v
	case map[string]any:
		for key, item := range v {
			v[key] = redactValue(item, sensitive)
		}
		return v
	default:
		return value
	}
}
