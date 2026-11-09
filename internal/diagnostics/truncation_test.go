package diagnostics

import (
	"context"
	"strings"
	"testing"
)

// A tool whose output passes outputLimit is recorded once per stage, and the
// stage gets an informative finding saying its result may be incomplete.
func TestHostRunnerRecordsCutOutput(t *testing.T) {
	env := HostEnv("pt-BR", 0)
	out, _, err := env.Run(context.Background(), "head", "-c", "33554500", "/dev/zero")
	if err != nil || len(out) != outputLimit {
		t.Fatalf("len %d err %v", len(out), err)
	}
	if _, _, err := env.Run(context.Background(), "head", "-c", "10", "/dev/zero"); err != nil {
		t.Fatal(err)
	}
	tools := env.TakeTruncated()
	if len(tools) != 1 || tools[0] != "head" {
		t.Fatalf("tools %v", tools)
	}
	if again := env.TakeTruncated(); len(again) != 0 {
		t.Fatalf("not cleared: %v", again)
	}
	finding := env.TruncationFinding(tools)
	if finding.Severity != Info || finding.Code != "stage.output-truncated" || !strings.Contains(finding.Text, "head") || !strings.Contains(finding.Text, "32 MiB") {
		t.Fatalf("%+v", finding)
	}
}

// Test environments built by hand have no recorder and never panic.
func TestTruncationRecorderIsOptional(t *testing.T) {
	env := &Env{}
	env.noteTruncated("rpm")
	if tools := env.TakeTruncated(); tools != nil {
		t.Fatalf("%v", tools)
	}
}
