package diagnostics

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestHostDiagnostics runs every implemented test against the real host and
// writes the stages as JSON to $VEGAD_HOST_DIAGNOSTICS_OUT. It needs root for
// rpm, smartctl and the full journal, so it only runs on request:
//
//	go test -c -o diag.test ./internal/diagnostics
//	sudo VEGAD_HOST_DIAGNOSTICS_OUT=/tmp/diag.json ./diag.test -test.run TestHostDiagnostics -test.v
//
// VEGAD_HOST_DIAGNOSTICS_TESTS (comma-separated ids) and
// VEGAD_HOST_DIAGNOSTICS_DURATION (stress seconds) narrow the run.
func TestHostDiagnostics(t *testing.T) {
	out := os.Getenv("VEGAD_HOST_DIAGNOSTICS_OUT")
	if out == "" {
		t.Skip("set VEGAD_HOST_DIAGNOSTICS_OUT to run the tests on this host")
	}
	env := HostEnv("pt-BR", 8)
	if seconds, err := strconv.Atoi(os.Getenv("VEGAD_HOST_DIAGNOSTICS_DURATION")); err == nil {
		env.DurationSeconds = seconds
	}
	env.Sample = func(s Sample) {
		data, _ := json.Marshal(s)
		t.Logf("sample %s", data)
	}
	only := os.Getenv("VEGAD_HOST_DIAGNOSTICS_TESTS")
	var stages []StageResult
	for _, test := range Tests {
		if only != "" && !strings.Contains(","+only+",", ","+test.ID+",") {
			continue
		}
		if err := test.Check(env); err != nil {
			t.Logf("%s: recusado (%v)", test.ID, err)
			continue
		}
		if ok, reason := test.Availability(env); !ok {
			t.Logf("%s: indisponível (%s)", test.ID, reason)
			continue
		}
		result := test.Run(context.Background(), env, func(percent uint32, message string) {
			t.Logf("%s %3d%% %s", test.ID, percent, message)
		})
		t.Logf("%s: %s, %s (%d s)", test.ID, result.Verdict, result.Summary, result.DurationSeconds)
		stages = append(stages, result)
	}
	data, err := json.MarshalIndent(stages, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
