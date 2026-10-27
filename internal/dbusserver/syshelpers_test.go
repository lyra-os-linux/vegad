package dbusserver

import (
	"os"
	"path/filepath"
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

func TestNmcliRunsWithUTF8Locale(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	cmd := systemCommand("nmcli", "-t", "-f", "NAME", "connection", "show")
	if got := cmd.Env[len(cmd.Env)-1]; got != "LC_ALL=C.UTF-8" {
		t.Fatalf("nmcli env ends with %q, want LC_ALL=C.UTF-8", got)
	}
	if other := systemCommand("snapper", "list"); other.Env != nil {
		t.Fatalf("snapper must keep the inherited environment, got %v", other.Env)
	}
}

func TestNmcliKeepsNonASCIIConnectionNames(t *testing.T) {
	bin := t.TempDir()
	// A stand-in for nmcli that, like the real one, degrades to "?" unless the
	// locale is UTF-8.
	script := "#!/bin/sh\ncase \"$LC_ALL\" in *UTF-8) printf 'Conex\\303\\243o cabeada 1\\n' ;; *) printf 'Conex?o cabeada 1\\n' ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "nmcli"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LC_ALL", "C")
	out, err := runCommandOutput("nmcli", "-t", "-f", "NAME", "connection", "show")
	if err != nil {
		t.Fatal(err)
	}
	if out != "Conexão cabeada 1" {
		t.Fatalf("nmcli output = %q", out)
	}
}
