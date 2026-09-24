package dbusserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lyraos/vegad/internal/profile"
)

// Run the actual RPM scriptlets against an isolated filesystem with inert
// system tools. This covers both specs without touching the host services.
func TestFirstUpdateRPMLifecycle(t *testing.T) {
	for _, spec := range []string{"vegad.spec", "vegad.obs.spec"} {
		t.Run(spec, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "..", "packaging", spec))
			if err != nil {
				t.Fatal(err)
			}
			for _, scenario := range []string{"fresh", "pending", "completed", "legacy", "legacy-completed", "exempted"} {
				t.Run(scenario, func(t *testing.T) {
					root := t.TempDir()
					prefix := filepath.Join(root, "usr")
					state := filepath.Join(root, "var", "lib", "vega")
					marker := filepath.Join(state, "first-update.done")
					skipped := filepath.Join(state, "first-update.skipped")
					unit := filepath.Join(prefix, "lib", "systemd", "system", "vegad-first-update.service")
					bin := filepath.Join(root, "bin")
					for _, dir := range []string{state, filepath.Dir(unit), bin} {
						if err := os.MkdirAll(dir, 0755); err != nil {
							t.Fatal(err)
						}
					}
					t.Setenv("VEGAD_FIRST_UPDATE_MARKER", marker)
					t.Setenv("VEGAD_TRUSTED_KEYRING", filepath.Join(root, "missing-keyring"))
					t.Setenv("VEGAD_UPDATE_STATE", filepath.Join(state, "update-status.json"))
					t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
					for _, name := range []string{"systemctl", "systemd-sysusers", "systemd-tmpfiles", "semodule", "selinuxenabled"} {
						script := "#!/bin/sh\nexit 0\n"
						if name == "selinuxenabled" {
							script = "#!/bin/sh\nexit 1\n"
						}
						if name == "systemctl" {
							// Enabling is allowed; starting preparation from an RPM transaction is not.
							script = "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$RPM_TEST_COMMANDS\"\n"
						}
						if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0755); err != nil {
							t.Fatal(err)
						}
					}
					commands := filepath.Join(root, "commands")
					t.Setenv("RPM_TEST_COMMANDS", commands)
					write := func(path, text string) {
						t.Helper()
						if err := os.WriteFile(path, []byte(text), 0644); err != nil {
							t.Fatal(err)
						}
					}
					if scenario == "pending" || scenario == "completed" || scenario == "exempted" {
						write(unit, "previous unit")
					}
					if scenario == "completed" || scenario == "legacy-completed" {
						write(marker, "original completion\n")
					}
					if scenario == "exempted" {
						write(skipped, "original exemption\n")
					}
					run := func(section, next, count string) {
						t.Helper()
						_, body, ok := strings.Cut(string(source), "\n%"+section+"\n")
						if !ok {
							t.Fatalf("missing %s", section)
						}
						body, _, ok = strings.Cut(body, "\n%"+next+"\n")
						if !ok {
							t.Fatalf("missing end of %s", section)
						}
						body = strings.NewReplacer("%{_prefix}", prefix, "%{_localstatedir}", filepath.Join(root, "var"), "%{_datadir}", filepath.Join(prefix, "share")).Replace(body)
						cmd := exec.Command("/bin/sh", "-c", body, "rpm-scriptlet", count)
						if out, err := cmd.CombinedOutput(); err != nil {
							t.Fatalf("%s: %v: %s", section, err, out)
						}
					}
					count := "2"
					if scenario == "fresh" {
						count = "1"
					}
					run("pre", "post", count)
					write(unit, "new unit") // RPM payload replacement happens between pre/post.
					run("post", "preun", count)
					if scenario == "fresh" || scenario == "pending" {
						// A failed preparation followed by another package upgrade must stay pending.
						var calls []string
						backend := preparationBackend{calls: &calls, fail: "refresh"}
						err := prepareInitialRepositories(profile.Desktop, marker, backend, func() error { return nil }, func(UpdateStatus) error { return nil })
						if err == nil {
							t.Fatal("expected refresh failure")
						}
						run("pre", "post", "2")
						run("post", "preun", "2")
						for _, path := range []string{marker, skipped} {
							if _, err := os.Stat(path); !os.IsNotExist(err) {
								t.Fatalf("pending preparation suppressed by %s: %v", path, err)
							}
						}
						backend.fail = ""
						if err := prepareInitialRepositories(profile.Desktop, marker, backend, func() error { return nil }, func(UpdateStatus) error { return nil }); err != nil {
							t.Fatal(err)
						}
						if data, err := os.ReadFile(marker); err != nil || string(data) != "done\n" {
							t.Fatalf("retry did not complete: %q, %v", data, err)
						}
					} else if scenario == "completed" || scenario == "legacy-completed" {
						if data, err := os.ReadFile(marker); err != nil || string(data) != "original completion\n" {
							t.Fatalf("completion changed: %q, %v", data, err)
						}
						if _, err := os.Stat(skipped); !os.IsNotExist(err) {
							t.Fatalf("completed installation exempted: %v", err)
						}
					} else {
						want := "legacy-installation\n"
						if scenario == "exempted" {
							want = "original exemption\n"
						}
						if data, err := os.ReadFile(skipped); err != nil || string(data) != want {
							t.Fatalf("exemption: %q, %v", data, err)
						}
						if _, err := os.Stat(marker); !os.IsNotExist(err) {
							t.Fatalf("exemption recorded as success: %v", err)
						}
						// The CLI must honor the same exemption as systemd, before key import.
						if err := RunFirstUpdateJob(profile.Desktop); err != nil {
							t.Fatal(err)
						}
					}
					log, err := os.ReadFile(commands)
					if err != nil {
						t.Fatal(err)
					}
					for _, line := range strings.Split(string(log), "\n") {
						if strings.Contains(line, "vegad-first-update.service") && (scenario != "fresh" || line != "enable vegad-first-update.service") {
							t.Fatalf("unexpected preparation activation: %s", line)
						}
					}
					for _, command := range []string{"enable vegad-trusted-keys.service\n", "--no-block start vegad-trusted-keys.service\n"} {
						if !strings.Contains(string(log), command) {
							t.Fatalf("missing independent key maintenance: %s", command)
						}
					}
					if scenario == "fresh" && !strings.Contains(string(log), "enable vegad-first-update.service\n") {
						t.Fatal("fresh install did not enable preparation")
					}
				})
			}
		})
	}
}
