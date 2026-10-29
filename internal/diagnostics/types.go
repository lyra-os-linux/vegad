// Package diagnostics runs vegad's health tests (vega#165, vegad#98) and
// turns tool output into verdicts, findings and metrics. The D-Bus job,
// signals and report storage live in internal/dbusserver; this package only
// knows how to run one test and parse what it printed, so every parser can be
// tested against real captures.
package diagnostics

import (
	"context"
	"strings"

	vegai18n "github.com/lyraos/vegad/internal/i18n"
)

// Verdict of a stage, and the aggregated result of a job.
type Verdict string

const (
	Ok          Verdict = "ok"
	Attention   Verdict = "attention"
	Fail        Verdict = "fail"
	Unavailable Verdict = "unavailable"
	Cancelled   Verdict = "cancelled"
	Aborted     Verdict = "aborted"
)

// Severity of a single finding. Info also holds classified known noise.
type Severity string

const (
	Info              Severity = "info"
	SeverityAttention Severity = "attention"
	SeverityFail      Severity = "fail"
)

// Finding is one observation. Code is stable; frontends translate and group
// by it. Text is already localized for the job locale.
type Finding struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Text     string   `json:"text"`
	Raw      string   `json:"raw,omitempty"`
}

// StageResult is the outcome of one test.
type StageResult struct {
	ID              string         `json:"id"`
	Verdict         Verdict        `json:"verdict"`
	Summary         string         `json:"summary"`
	DurationSeconds int            `json:"durationSeconds"`
	Findings        []Finding      `json:"findings"`
	Metrics         map[string]any `json:"metrics,omitempty"`
	// Sensitive values (serial numbers, hostnames) that a redacted report
	// must hide. Never serialized with the stage.
	Sensitive []string `json:"-"`
}

// Progress reports the current stage percentage and a short message.
type Progress func(percent uint32, message string)

// Env carries everything a test touches on the host, so tests can replace it.
type Env struct {
	Locale    string
	BootsBack int
	Run       Runner
	ReadFile  func(string) ([]byte, error)
	Glob      func(string) ([]string, error)
	LookPath  func(string) (string, error)
}

func (e *Env) t(key string, args ...string) string {
	text := vegai18n.T(e.Locale, key)
	for i := 0; i+1 < len(args); i += 2 {
		text = strings.ReplaceAll(text, "{"+args[i]+"}", args[i+1])
	}
	return text
}

func (e *Env) finding(severity Severity, code, raw string, args ...string) Finding {
	return Finding{Severity: severity, Code: code, Text: e.t("diagnostics.finding."+code, args...), Raw: raw}
}

// TestFunc runs one test. It must honour ctx: cancellation and the stage
// timeout both arrive through it.
type TestFunc func(ctx context.Context, env *Env, progress Progress) StageResult

// verdictFromFindings returns the worst severity among findings.
func verdictFromFindings(findings []Finding) Verdict {
	verdict := Ok
	for _, f := range findings {
		switch f.Severity {
		case SeverityFail:
			return Fail
		case SeverityAttention:
			verdict = Attention
		}
	}
	return verdict
}

// Aggregate combines stage verdicts into the job result: cancellation and
// safeguard aborts win, then the worst health verdict; unavailable stages do
// not count.
func Aggregate(stages []StageResult) Verdict {
	result := Ok
	rank := map[Verdict]int{Ok: 0, Attention: 1, Fail: 2}
	for _, stage := range stages {
		switch stage.Verdict {
		case Cancelled:
			return Cancelled
		case Aborted:
			result = Aborted
		case Ok, Attention, Fail:
			if result != Aborted && rank[stage.Verdict] > rank[result] {
				result = stage.Verdict
			}
		}
	}
	return result
}
