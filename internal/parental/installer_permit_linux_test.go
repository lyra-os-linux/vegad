package parental

import (
	"strings"
	"testing"
)

func TestEnrollmentOperationFromPermitDistinguishesAvailableAndConsumed(t *testing.T) {
	installation, operation := strings.Repeat("1", 32), strings.Repeat("2", 32)
	for _, tc := range []struct {
		name     string
		permit   installerPermit
		consumed bool
	}{
		{name: "available", permit: installerPermit{installation: installation, operation: operation, state: installerPermitAvailable}},
		{name: "consumed", permit: installerPermit{installation: installation, operation: operation, state: installerPermitConsumed, digest: strings.Repeat("a", 64)}, consumed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			operationID, consumed, err := enrollmentOperationFromPermit(permitBytes(tc.permit), installation)
			if err != nil || operationID != operation || consumed != tc.consumed {
				t.Fatalf("permit result=(%q,%t), error=%v", operationID, consumed, err)
			}
		})
	}
}

func TestEnrollmentOperationFromPermitRejectsMismatchAndMalformedRecord(t *testing.T) {
	installation, operation := strings.Repeat("1", 32), strings.Repeat("2", 32)
	available := permitBytes(installerPermit{installation: installation, operation: operation, state: installerPermitAvailable})
	for name, data := range map[string][]byte{
		"wrong installation": available,
		"malformed":          []byte(`{"schema":1}`),
	} {
		t.Run(name, func(t *testing.T) {
			requested := installation
			if name == "wrong installation" {
				requested = strings.Repeat("3", 32)
			}
			if _, _, err := enrollmentOperationFromPermit(data, requested); err == nil {
				t.Fatal("invalid permit was accepted")
			}
		})
	}
}
