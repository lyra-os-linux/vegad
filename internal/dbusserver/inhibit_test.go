package dbusserver

import (
	"strings"
	"testing"
)

func TestSanitizeInhibitorReason(t *testing.T) {
	if got := sanitizeInhibitorReason("  Backup\nconfig\t42  "); got != "Backupconfig42" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeInhibitorReason(""); got != "operação do Vega" {
		t.Fatalf("empty reason: %q", got)
	}
	if got := sanitizeInhibitorReason(strings.Repeat("x", inhibitorReasonLimit+20)); len(got) != inhibitorReasonLimit {
		t.Fatalf("reason length=%d", len(got))
	}
}
