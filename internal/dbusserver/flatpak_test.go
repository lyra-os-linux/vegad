package dbusserver

import (
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

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
