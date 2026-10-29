package diagnostics

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// TestHostDiagnostics runs every implemented test against the real host and
// writes the stages as JSON to $VEGAD_HOST_DIAGNOSTICS_OUT. It needs root for
// rpm, smartctl and the full journal, so it only runs on request:
//
//	go test -c -o diag.test ./internal/diagnostics
//	sudo VEGAD_HOST_DIAGNOSTICS_OUT=/tmp/diag.json ./diag.test -test.run TestHostDiagnostics -test.v
func TestHostDiagnostics(t *testing.T) {
	out := os.Getenv("VEGAD_HOST_DIAGNOSTICS_OUT")
	if out == "" {
		t.Skip("set VEGAD_HOST_DIAGNOSTICS_OUT to run the tests on this host")
	}
	env := HostEnv("pt-BR", 8)
	var stages []StageResult
	for _, test := range Tests {
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
