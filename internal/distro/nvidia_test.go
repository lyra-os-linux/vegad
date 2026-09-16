package distro

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const guardPlan = `<install-summary packages-to-change="1"><to-install><solvable type="package" name="lyra-nvidia" edition="610.57.04-lp161.1.1" arch="noarch" repository="lyra-nvidia"/></to-install></install-summary>`
const commitPrompt = `<prompt id="0"><option value="y"/><option value="n"/></prompt>`

func TestNvidiaInstallProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, body                 string
		callbackErr, errorExpected bool
	}{
		{"adopt", guardPlan + commitPrompt, false, false},
		{"cancel-before-commit", guardPlan + commitPrompt, true, true},
		{"no-summary", commitPrompt, false, true},
		{"truncated", guardPlan, false, true},
		{"remove", strings.ReplaceAll(guardPlan, "to-install", "to-remove") + commitPrompt, false, true},
		{"upgrade", strings.ReplaceAll(guardPlan, "to-install", "to-upgrade") + commitPrompt, false, true},
		{"downgrade", strings.ReplaceAll(guardPlan, "to-install", "to-downgrade") + commitPrompt, false, true},
		{"new-version", strings.ReplaceAll(guardPlan, "610.57.04", "615.71.09") + commitPrompt, false, true},
		{"wrong-repository", strings.ReplaceAll(guardPlan, `repository="lyra-nvidia"`, `repository="evil"`) + commitPrompt, false, true},
		{"wrong-arch", strings.ReplaceAll(guardPlan, "noarch", "x86_64") + commitPrompt, false, true},
		{"kernel-change", strings.ReplaceAll(guardPlan, `name="lyra-nvidia"`, `name="kernel-default"`) + commitPrompt, false, true},
		{"dkms", strings.ReplaceAll(guardPlan, `name="lyra-nvidia"`, `name="nvidia-open-driver-G07"`) + commitPrompt, false, true},
		{"gpg-prompt", guardPlan + strings.ReplaceAll(commitPrompt, `id="0"`, `id="14"`), false, true},
		{"solver-prompt", guardPlan + strings.ReplaceAll(commitPrompt, `id="0"`, `id="1"`), false, true},
		{"second-prompt", guardPlan + commitPrompt + commitPrompt, false, true},
		{"changed-plan", guardPlan + guardPlan + commitPrompt, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			called := false
			err := answerNvidiaInstall(strings.NewReader("<stream>"+tc.body+"</stream>"), &out, func() error {
				called = true
				if tc.callbackErr {
					return errors.New("cancel")
				}
				return nil
			}, func(uint32, string) {})
			if (err != nil) != tc.errorExpected {
				t.Fatalf("err=%v reply=%q", err, out.String())
			}
			if !tc.errorExpected && (!called || out.String() != "y\n") {
				t.Fatalf("missing commit: %q", out.String())
			}
			if tc.errorExpected && tc.name != "second-prompt" && out.Len() != 0 {
				t.Fatalf("unsafe plan approved: %q", out.String())
			}
		})
	}
}

func TestNvidiaFreshBundlePlan(t *testing.T) {
	rows := `<solvable type="package" name="lyra-nvidia" edition="610.57.04-lp161.1.1" arch="noarch" repository="lyra-nvidia"/>`
	for _, name := range []string{"nvidia-open", "nvidia-common-G07", "nvidia-compute-G07", "nvidia-compute-utils-G07", "nvidia-gl-G07", "nvidia-video-G07", "nvidia-modprobe", "nvidia-persistenced"} {
		rows += `<solvable type="package" name="` + name + `" edition="610.57.04-1" arch="x86_64" repository="lyra-nvidia-upstream"/>`
	}
	rows += `<solvable type="package" name="` + NvidiaKMP + `" edition="610.57.04_k6.12.0_160100.4-160100.1.3" arch="x86_64" repository="lyra-nvidia-oss"/>`
	body := `<stream><install-summary packages-to-change="10"><to-install>` + rows + `</to-install></install-summary>` + commitPrompt + `</stream>`
	var out bytes.Buffer
	if err := answerNvidiaInstall(strings.NewReader(body), &out, func() error { return nil }, func(uint32, string) {}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "y\n" {
		t.Fatal(out.String())
	}
}

func TestNvidiaRepoPolicyPreservesAdministratorChoices(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		blocked    bool
	}{
		{"valid", nvidiaRepoDefinition("lyra-nvidia", NvidiaOBSURL), false},
		{"disabled", strings.ReplaceAll(nvidiaRepoDefinition("lyra-nvidia", NvidiaOBSURL), "enabled=1", "enabled=0"), true},
		{"unverified", strings.ReplaceAll(nvidiaRepoDefinition("lyra-nvidia", NvidiaOBSURL), "repo_gpgcheck=1", "repo_gpgcheck=0"), true},
		{"substitution", strings.ReplaceAll(nvidiaRepoDefinition("lyra-nvidia", NvidiaOBSURL), NvidiaOBSURL, "https://example.com"), true},
		{"competing", nvidiaRepoDefinition("other-nvidia", NvidiaUpstreamURL), true},
		{"old-disabled", "[repo-nvidia]\nenabled=0\nbaseurl=https://download.nvidia.com/opensuse/leap/16.0/\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "lyra-nvidia.repo")
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			err := ValidateNvidiaRepositories(dir)
			if (err != nil) != tc.blocked {
				t.Fatalf("%v", err)
			}
			data, _ := os.ReadFile(path)
			if string(data) != tc.data {
				t.Fatal("changed administrator config")
			}
		})
	}
}

func TestNvidiaDoesNotOverwriteOBSRepo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lyra-nvidia.repo")
	original := nvidiaRepoDefinition("lyra-nvidia", NvidiaOBSURL) + "# admin comment\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := EnableNvidiaOBS(dir); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != original {
		t.Fatal("overwrote existing repo")
	}
}

// Opt-in test using a disposable RPM database and real Zypper. The caller
// must bind the host read-only and provide writable fixture/cache directories.
func TestNvidiaNativeZypperReview(t *testing.T) {
	root := os.Getenv("VEGA_NVIDIA_NATIVE_ROOT")
	if root == "" {
		t.Skip("requires disposable native fixture")
	}
	log, err := os.Create(filepath.Join(root, "native-protocol.xml"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := nvidiaCommand(ctx, "--root", root, "--xmlout", "--no-refresh", "install", "--dry-run", "--no-recommends", "--", "lyra-nvidia="+NvidiaVersion)
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	approved := false
	cancelled := os.Getenv("VEGA_NVIDIA_NATIVE_CANCEL") == "1"
	err = answerNvidiaInstall(io.TeeReader(output, log), input, func() error {
		approved = true
		if cancelled {
			return errors.New("fixture cancellation")
		}
		return nil
	}, func(uint32, string) {})
	input.Close()
	if err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	waitErr := cmd.Wait()
	if cancelled {
		if !approved || err == nil || !strings.Contains(err.Error(), "fixture cancellation") {
			t.Fatalf("cancellation: approved=%v parse=%v process=%v", approved, err, waitErr)
		}
	} else if err != nil || waitErr != nil || !approved {
		t.Fatalf("approved=%v parse=%v process=%v", approved, err, waitErr)
	}
}

func TestNvidiaFailedCommitDisablesOnlyManagedUpstream(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lyra-nvidia-upstream.repo")
	if err := os.WriteFile(path, []byte(nvidiaRepoDefinition("lyra-nvidia-upstream", NvidiaUpstreamURL)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := DisableUnprotectedNvidiaRepository(dir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "enabled=0\n") {
		t.Fatal("unguarded source still enabled")
	}
	foreign := strings.ReplaceAll(nvidiaRepoDefinition("lyra-nvidia-upstream", NvidiaUpstreamURL), NvidiaUpstreamURL, "https://example.com/")
	if err := os.WriteFile(path, []byte(foreign), 0644); err != nil {
		t.Fatal(err)
	}
	if err := DisableUnprotectedNvidiaRepository(dir); err == nil {
		t.Fatal("accepted changed config")
	}
	data, _ = os.ReadFile(path)
	if string(data) != foreign {
		t.Fatal("changed administrator config")
	}
}

func TestNvidiaOBSRepoReadableDespiteDaemonUmask(t *testing.T) {
	if os.Getenv("VEGA_NVIDIA_UMASK_CHILD") == "1" {
		syscall.Umask(0077)
		dir := t.TempDir()
		if err := EnableNvidiaOBS(dir); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(dir, "lyra-nvidia.repo"))
		if err != nil || info.Mode().Perm() != 0644 {
			t.Fatalf("repository not readable by query workers: %v %v", info, err)
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestNvidiaOBSRepoReadableDespiteDaemonUmask$")
	cmd.Env = append(os.Environ(), "VEGA_NVIDIA_UMASK_CHILD=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
}
