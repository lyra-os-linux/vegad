package main

import (
	"os"
	"strings"
	"testing"
)

func TestEnsureSystemPATH(t *testing.T) {
	tests := []struct {
		name    string
		initial string
		unset   bool
		want    []string // dirs that must be present afterwards
	}{
		{name: "empty PATH gets the full default", initial: "", want: strings.Split(defaultSystemPATH, ":")},
		{name: "unset PATH gets the full default", unset: true, want: strings.Split(defaultSystemPATH, ":")},
		{name: "partial PATH is topped up, not replaced", initial: "/opt/custom/bin", want: append([]string{"/opt/custom/bin"}, strings.Split(defaultSystemPATH, ":")...)},
		{name: "already-complete PATH is left alone content-wise", initial: defaultSystemPATH, want: strings.Split(defaultSystemPATH, ":")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old, hadOld := os.LookupEnv("PATH")
			defer func() {
				if hadOld {
					os.Setenv("PATH", old)
				} else {
					os.Unsetenv("PATH")
				}
			}()

			if tt.unset {
				os.Unsetenv("PATH")
			} else {
				os.Setenv("PATH", tt.initial)
			}

			ensureSystemPATH()

			got := os.Getenv("PATH")
			for _, dir := range tt.want {
				if !strings.Contains(":"+got+":", ":"+dir+":") {
					t.Fatalf("ensureSystemPATH() = %q, missing expected dir %q", got, dir)
				}
			}
		})
	}
}
