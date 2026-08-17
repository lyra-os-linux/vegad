package dbusserver

import (
	"reflect"
	"testing"
)

func TestSystemCommandIsolatesPublicUmaskToZypper(t *testing.T) {
	cmd := systemCommand("zypper", "search", "vim")
	want := []string{"/bin/sh", "-c", `umask 0022; exec /usr/bin/zypper "$@"`, "vega-zypper", "search", "vim"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("unexpected command: %#v", cmd.Args)
	}
}
