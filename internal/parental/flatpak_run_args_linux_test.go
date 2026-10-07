package parental

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestBuildFlatpakRunArgsAreExactAndClosed(t *testing.T) {
	app := InstalledFlatpak{Ref: "app/org.lyra.TestApp/x86_64/stable", Scope: "system",
		Installation: "system", Commit: strings.Repeat("a", 64),
		RuntimeRef: "runtime/org.lyra.TestRuntime/x86_64/stable", RuntimeCommit: strings.Repeat("b", 64)}
	want := []string{"run", "--system", "--arch=x86_64", "--branch=stable",
		"--runtime=org.lyra.TestRuntime", "--runtime-version=stable",
		"--commit=" + app.Commit, "--runtime-commit=" + app.RuntimeCommit, "org.lyra.TestApp"}
	got, err := BuildFlatpakRunArgs(ResolvedFlatpak{app}, 1003)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected argv: %q %v", got, err)
	}
	app.Scope, app.Installation, app.OwnerUID = "user", "user", 1003
	want[1] = "--user"
	got, err = BuildFlatpakRunArgs(ResolvedFlatpak{app}, 1003)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("wrong user argv: %q %v", got, err)
	}
	app.Scope, app.Installation, app.OwnerUID = "system", "secondary", 0
	want[1] = "--installation=secondary"
	got, err = BuildFlatpakRunArgs(ResolvedFlatpak{app}, 1003)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("wrong installation argv: %q %v", got, err)
	}
	for name, mutate := range map[string]func(*InstalledFlatpak){
		"other-user":          func(a *InstalledFlatpak) { a.Scope, a.OwnerUID = "user", 1011 },
		"bad-ref":             func(a *InstalledFlatpak) { a.Ref = "app/org.lyra.TestApp/x86_64/../stable" },
		"short-commit":        func(a *InstalledFlatpak) { a.Commit = strings.Repeat("a", 12) },
		"runtime-arch":        func(a *InstalledFlatpak) { a.RuntimeRef = "runtime/org.lyra.TestRuntime/aarch64/stable" },
		"option-installation": func(a *InstalledFlatpak) { a.Installation = "--app-fd=9" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := app
			mutate(&bad)
			if _, err := BuildFlatpakRunArgs(ResolvedFlatpak{bad}, 1003); !errors.Is(err, UnresolvedFlatpakDeployment) {
				t.Fatalf("unsafe argv generated: %v", err)
			}
		})
	}
}
