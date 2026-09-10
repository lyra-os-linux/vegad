package dbusserver

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNMCLIFieldsPreserveEscapesAndEmptyValues(t *testing.T) {
	for _, test := range []struct {
		line string
		want []string
	}{
		{`*:Office\:Guest:WPA2:90:wlan0`, []string{"*", "Office:Guest", "WPA2", "90", "wlan0"}},
		{`*:Rede São Paulo\\Casa\:5G::90:wlan0`, []string{"*", `Rede São Paulo\Casa:5G`, "", "90", "wlan0"}},
		{`::`, []string{"", "", ""}},
		{`a\\:b`, []string{`a\`, "b"}},
		{`a\q:b\`, []string{`a\q`, `b\`}},
	} {
		if got := splitNM(test.line); !reflect.DeepEqual(got, test.want) {
			t.Errorf("%q = %#v, want %#v", test.line, got, test.want)
		}
	}
	if got := valueAfterColon(`IP6.ADDRESS[1]:fe80\:\:1234/64`); got != "fe80::1234/64" {
		t.Fatalf("IPv6 = %q", got)
	}
}

func TestDisableServicePropagatesEveryCommandFailure(t *testing.T) {
	for _, diagnostic := range []string{"Failed to stop service", "Failed to disable service", "Unit does not exist"} {
		t.Run(diagnostic, func(t *testing.T) {
			bin := t.TempDir()
			t.Setenv("PATH", bin)
			t.Setenv("LYRA_TEST_SERVICE_ERROR", diagnostic)
			stub := "#!/bin/sh\nprintf '%s' \"$LYRA_TEST_SERVICE_ERROR\" >&2\nexit 1\n"
			if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(stub), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := setServiceEnabled("fixture.service", false); err == nil || !strings.Contains(err.Error(), diagnostic) {
				t.Fatalf("failed disable reported %v", err)
			}
		})
	}
}
