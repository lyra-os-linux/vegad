package dbusserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/profile"
)

func TestFlatpakMutationRejectsOptionsAndNonIDsBeforeExecution(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, id := range []string{"", "--all", "-y", "org.example.*", "org.example.App/stable", "org.example.App\n", " org.example.App", "https://example.org/app.flatpakref", "org.example", "org..App", "org.bad-domain.App", strings.Repeat("a", 256)} {
		for _, operation := range []string{"install", "uninstall", "update"} {
			if _, err := flatpakAppCommand(operation, id, "system", nil); err == nil {
				t.Errorf("%s accepted %q", operation, id)
			}
		}
		s := &SoftwareService{activity: &Activity{}, profile: profile.Desktop}
		for _, method := range []func() (uint32, *dbus.Error){
			func() (uint32, *dbus.Error) { return s.Install(":1.2", "flathub", id) },
			func() (uint32, *dbus.Error) { return s.Remove(":1.2", "flathub", id) },
			func() (uint32, *dbus.Error) { return s.UpdatePackage(":1.2", "flathub", id) },
		} {
			if tx, err := method(); tx != 0 || err == nil || !strings.Contains(err.Error(), "identificador") {
				t.Errorf("invalid id %q reached authorization/transaction: %d %v", id, tx, err)
			}
		}
	}
}

func TestFlatpakAppCommandsSeparateArgumentsAndKeepUserIdentity(t *testing.T) {
	u := &desktopUser{Uid: 1001, Username: "alice", HomeDir: "/home/alice", RuntimeDir: t.TempDir()}
	for _, id := range []string{"org.mozilla.firefox", "io.github.example.App-name", "org.freedesktop.Platform.GL.default"} {
		for _, operation := range []string{"install", "uninstall", "update"} {
			cmd, err := flatpakAppCommand(operation, id, "system", nil)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"flatpak", operation, "-y", "--noninteractive", "--app", "--system", "--"}
			if operation == "install" {
				want = append(want, "flathub")
			}
			want = append(want, id)
			if !reflect.DeepEqual(cmd.Args, want) {
				t.Fatalf("args = %#v, want %#v", cmd.Args, want)
			}
		}
		cmd, err := flatpakAppCommand("uninstall", id, "user", u)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"/usr/sbin/runuser", "--user", "alice", "--", "/usr/bin/flatpak", "uninstall", "-y", "--noninteractive", "--app", "--user", "--", id}
		if !reflect.DeepEqual(cmd.Args, want) {
			t.Fatalf("user args = %#v", cmd.Args)
		}
		assertSingleEnvValue(t, cmd.Env, "HOME", "/home/alice")
		if _, err := flatpakAppCommand("uninstall", id, "user", nil); err == nil {
			t.Fatal("unresolved user fell back to system scope")
		}
	}
}

func TestFlatpakSystemInstallRequiresPolkit(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	argsFile := filepath.Join(bin, "args")
	t.Setenv("FLATPAK_TEST_POLKIT_ARGS", argsFile)
	stub := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$FLATPAK_TEST_POLKIT_ARGS\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "pkcheck"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	s := &SoftwareService{activity: &Activity{}, profile: profile.Desktop}
	if tx, err := s.Install(":1.42", "flathub", "org.example.App"); tx != 0 || err == nil || err.Name != BusName+".Error.AuthorizationFailed" {
		t.Fatalf("unauthorized install = %d %v", tx, err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil || !strings.Contains(string(args), "org.lyraos.vega.software.install\n--system-bus-name\n:1.42\n") {
		t.Fatalf("authorization did not bind the action and caller: %q %v", args, err)
	}
}

func TestFlatpakUserCmdUsesRunuserOutsideFork(t *testing.T) {
	u := &desktopUser{
		Uid:        1001,
		Gid:        1001,
		Username:   "alice",
		HomeDir:    "/home/alice",
		RuntimeDir: t.TempDir(),
	}

	cmd := flatpakUserCmd(u, "update", "--user")
	wantArgs := []string{
		"/usr/sbin/runuser", "--user", "alice", "--",
		"/usr/bin/flatpak", "update", "--user",
	}
	if cmd.Path != "/usr/sbin/runuser" {
		t.Fatalf("Path = %q, want /usr/sbin/runuser", cmd.Path)
	}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("Args = %#v, want %#v", cmd.Args, wantArgs)
	}
	if cmd.SysProcAttr != nil {
		t.Fatalf("SysProcAttr = %#v, want nil: credential changes during fork cause EPERM", cmd.SysProcAttr)
	}
	assertSingleEnvValue(t, cmd.Env, "HOME", "/home/alice")
	assertSingleEnvValue(t, cmd.Env, "XDG_RUNTIME_DIR", u.RuntimeDir)
}

func TestEnvironmentWithReplacesDuplicateValues(t *testing.T) {
	env := []string{"PATH=/usr/bin", "HOME=/root", "HOME=/old"}
	got := environmentWith(env, "HOME", "/home/alice")
	assertSingleEnvValue(t, got, "HOME", "/home/alice")
}

func assertSingleEnvValue(t *testing.T, env []string, key, want string) {
	t.Helper()
	prefix := key + "="
	var values []string
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			values = append(values, strings.TrimPrefix(entry, prefix))
		}
	}
	if !reflect.DeepEqual(values, []string{want}) {
		t.Fatalf("%s values = %#v, want [%q] (process HOME=%q)", key, values, want, os.Getenv(key))
	}
}

func TestSearchFlatpakFindsFirefox(t *testing.T) {
	if _, err := exec.LookPath("flatpak"); err != nil {
		t.Skip("flatpak é opcional e não está instalado")
	}
	remotes, err := exec.Command("flatpak", "remotes", "--system", "--columns=name").Output()
	if err != nil || !strings.Contains(string(remotes), "flathub") {
		t.Skip("o remote Flathub do sistema é necessário para este teste de integração")
	}

	results, err := searchFlatpak("firefox", nil)
	if err != nil {
		t.Fatalf("searchFlatpak: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Origin != "flathub" {
			t.Fatalf("unexpected origin %q on result %+v", r.Origin, r)
		}
		if r.Id == "org.mozilla.firefox" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected to find org.mozilla.firefox in results, got %+v", results)
	}
}

// The plan is a table whose ID column holds the bare app ID; matching the raw
// substring instead let a longer ID's row mark its prefix as pending too.
func TestPlanListsAppMatchesWholeIDsOnly(t *testing.T) {
	plan := []byte("Procurando por atualizações…\n\n" +
		"        ID                            Ramo    Op\n" +
		" 1. [ ] org.gnome.Builder.Devel       master   u\n\n" +
		"Deseja continuar? [S/n]:")

	if !planListsApp(plan, "org.gnome.Builder.Devel") {
		t.Fatal("o app listado no plano deve casar")
	}
	if planListsApp(plan, "org.gnome.Builder") {
		t.Fatal("prefixo de outro ID não pode ser lido como pendente")
	}
}

// Some flatpak outputs print the full ref instead of the bare ID.
func TestPlanListsAppMatchesFullRefSegments(t *testing.T) {
	plan := []byte(" 1. [ ] app/us.zoom.Zoom/x86_64/stable   u\n")
	if !planListsApp(plan, "us.zoom.Zoom") {
		t.Fatal("segmento de uma ref completa deve casar")
	}
	if planListsApp(plan, "x86_64") {
		t.Fatal("arquitetura não é um ID de aplicativo pendente")
	}
}

// An empty plan must not mark anything pending.
func TestPlanListsAppIgnoresEmptyPlan(t *testing.T) {
	if planListsApp([]byte("Procurando por atualizações…\n\nNada para fazer.\n"), "us.zoom.Zoom") {
		t.Fatal("plano vazio não pode marcar pendências")
	}
}
