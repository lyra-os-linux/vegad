package dbusserver

import (
	"reflect"
	"strings"
	"testing"
)

func TestCheckSupervisionRefusesSupervisedAdministrators(t *testing.T) {
	err := checkSupervision("ana", []string{"users", "wheel", supervisedGroup}, map[string]bool{"pai": true})
	if err == nil || !strings.Contains(err.Error(), "não pode ser administradora") {
		t.Fatalf("supervised administrator accepted: %v", err)
	}
	if err := checkSupervision("ana", []string{"users", supervisedGroup}, map[string]bool{"pai": true}); err != nil {
		t.Fatalf("supervised account refused: %v", err)
	}
}

func TestCheckSupervisionKeepsTheLastAdministrator(t *testing.T) {
	admins := map[string]bool{"pai": true, "root": true}
	err := checkSupervision("pai", []string{"users", supervisedGroup}, admins)
	if err == nil || !strings.Contains(err.Error(), "última conta administradora") {
		t.Fatalf("last administrator became supervised: %v", err)
	}
	if err := checkSupervision("pai", []string{"users"}, admins); err == nil {
		t.Fatal("last administrator lost wheel")
	}
	admins["mae"] = true
	if err := checkSupervision("pai", []string{"users"}, admins); err != nil {
		t.Fatalf("second administrator could not step down: %v", err)
	}
	if err := checkSupervision("pai", []string{"users", "wheel"}, map[string]bool{"pai": true}); err != nil {
		t.Fatalf("keeping the last administrator was refused: %v", err)
	}
}

func TestSupervisionPathsKeepTheAdministratorEntries(t *testing.T) {
	entries := []string{"x-scheme-handler/http", "/usr/bin/kgx", "app/org.gnome.Calculator/x86_64/stable"}
	got := withSupervisionPaths(false, entries, true)
	want := []string{"x-scheme-handler/http", "app/org.gnome.Calculator/x86_64/stable",
		"/usr/bin/kgx", "/usr/bin/gnome-terminal", "/usr/bin/fish", "/usr/bin/vega-gtk"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("supervise = %v, want %v", got, want)
	}
	got = withSupervisionPaths(false, got, false)
	want = []string{"x-scheme-handler/http", "app/org.gnome.Calculator/x86_64/stable"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unsupervise = %v, want %v", got, want)
	}
	allow := []string{"/usr/bin/gnome-calculator"}
	if got := withSupervisionPaths(true, allow, true); !reflect.DeepEqual(got, allow) {
		t.Fatalf("allowlist changed: %v", got)
	}
}
