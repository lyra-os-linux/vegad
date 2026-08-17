package distro

import (
	"reflect"
	"testing"
)

func TestPackageCommandIsolatesPublicUmaskToZypper(t *testing.T) {
	cmd := packageCommand("zypper", "--non-interactive", "refresh")
	want := []string{"/bin/sh", "-c", `umask 0022; exec /usr/bin/zypper "$@"`, "vega-zypper", "--non-interactive", "refresh"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("unexpected command: %#v", cmd.Args)
	}

	other := packageCommand("rpm", "-qa")
	if !reflect.DeepEqual(other.Args, []string{"rpm", "-qa"}) {
		t.Fatalf("non-zypper command was wrapped: %#v", other.Args)
	}
}
