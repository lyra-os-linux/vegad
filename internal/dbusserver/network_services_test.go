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

func TestParseConditionSkippedOnlyFlagsCheckedUnmetConditions(t *testing.T) {
	out := strings.Join([]string{
		"Id=bluetooth.service\nActiveState=inactive\nConditionResult=no\nConditionTimestampMonotonic=4899624",
		"Id=cups.service\nActiveState=active\nConditionResult=yes\nConditionTimestampMonotonic=6467410",
		"Id=never-started.service\nActiveState=inactive\nConditionResult=no\nConditionTimestampMonotonic=0",
		"Id=crashed.service\nActiveState=failed\nConditionResult=yes\nConditionTimestampMonotonic=5000",
		"Id=stopped.service\nActiveState=inactive\nConditionResult=yes\nConditionTimestampMonotonic=5000",
	}, "\n\n")
	got, unchecked := parseConditionSkipped(out, []string{"dbus-org.bluez.service", "cups.service", "never-started.service", "crashed.service", "stopped.service"})
	want := map[string]bool{"dbus-org.bluez.service": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseConditionSkipped = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(unchecked, []string{"never-started.service"}) {
		t.Fatalf("unchecked = %v, want [never-started.service]", unchecked)
	}
}

func TestParseConditionSkippedFallsBackToIdWhenBlocksMismatch(t *testing.T) {
	out := "Id=bluetooth.service\nActiveState=inactive\nConditionResult=no\nConditionTimestampMonotonic=42"
	got, _ := parseConditionSkipped(out, []string{"a.service", "b.service"})
	if !reflect.DeepEqual(got, map[string]bool{"bluetooth.service": true}) {
		t.Fatalf("parseConditionSkipped = %v", got)
	}
}

func TestConditionsMetEvaluatesUncheckedPathConditions(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	empty := filepath.Join(dir, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		conditions []unitCondition
		want       bool
	}{
		{"no conditions", nil, true},
		{"missing directory", []unitCondition{{Type: "ConditionPathIsDirectory", Param: missing}}, false},
		{"existing directory", []unitCondition{{Type: "ConditionPathIsDirectory", Param: dir}}, true},
		{"negated missing path", []unitCondition{{Type: "ConditionPathExists", Negate: true, Param: missing}}, true},
		{"empty directory", []unitCondition{{Type: "ConditionDirectoryNotEmpty", Param: empty}}, false},
		{"already failed", []unitCondition{{Type: "ConditionPathExists", Param: dir, State: -1}}, false},
		{"unknown type counts as met", []unitCondition{{Type: "ConditionVirtualization", Param: "!container"}}, true},
		{"one trigger met", []unitCondition{
			{Type: "ConditionPathExists", Trigger: true, Param: missing},
			{Type: "ConditionPathExists", Trigger: true, Param: dir},
		}, true},
		{"no trigger met", []unitCondition{
			{Type: "ConditionPathExists", Trigger: true, Param: missing},
			{Type: "ConditionPathIsDirectory", Trigger: true, Param: missing},
		}, false},
	} {
		if got := conditionsMet(test.conditions); got != test.want {
			t.Errorf("%s: conditionsMet = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestSystemdUnitPathEscapesLikeSystemd(t *testing.T) {
	for name, want := range map[string]string{
		"bluetooth.service":      "/org/freedesktop/systemd1/unit/bluetooth_2eservice",
		"systemd-pstore.service": "/org/freedesktop/systemd1/unit/systemd_2dpstore_2eservice",
		"9p.service":             "/org/freedesktop/systemd1/unit/_39p_2eservice",
	} {
		if got := string(systemdUnitPath(name)); got != want {
			t.Errorf("systemdUnitPath(%q) = %q, want %q", name, got, want)
		}
	}
}
