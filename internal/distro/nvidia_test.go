package distro

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const guardPlan = `<install-summary packages-to-change="1"><to-install><solvable type="package" name="lyra-nvidia" edition="615.71.09-lp161.1.1" arch="noarch" repository="lyra-nvidia"/></to-install></install-summary>`
const testKMPEdition = NvidiaVersion + "_k6.12.0_160100.7-"
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
		{"new-version", strings.ReplaceAll(guardPlan, NvidiaVersion, "620.10.01") + commitPrompt, false, true},
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
			err := answerNvidiaInstall(strings.NewReader("<stream>"+tc.body+"</stream>"), &out, testKMPEdition, func() error {
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

func freshBundlePlan(kmpEdition string) string {
	return freshBundlePlanWith(kmpEdition, "")
}

// Dry-run on the GTX 1650 reference (2026-10-13, no NVIDIA packages installed):
// nvidia-gl-G07 pulls the three EGL platform libraries and nvidia-compute-G07
// pulls libOpenCL1, all at their own versions.
const runtimeLibraryRows = `<solvable type="package" name="libnvidia-egl-gbm1" edition="1.1.3-160100.3.1" arch="x86_64" repository="lyra-nvidia-oss"/>` +
	`<solvable type="package" name="libnvidia-egl-wayland21" edition="1.0.2-1" arch="x86_64" repository="lyra-nvidia-upstream"/>` +
	`<solvable type="package" name="libnvidia-egl-x111" edition="1.0.6-160100.1.1" arch="x86_64" repository="lyra-nvidia-oss"/>` +
	`<solvable type="package" name="libOpenCL1" edition="2.3.1-160100.2.1" arch="x86_64" repository="lyra-nvidia-oss"/>`

func freshBundlePlanWith(kmpEdition, extraRows string) string {
	rows := `<solvable type="package" name="lyra-nvidia" edition="` + NvidiaVersion + `-lp161.1.1" arch="noarch" repository="lyra-nvidia"/>`
	for _, name := range []string{"nvidia-open", "nvidia-common-G07", "nvidia-compute-G07", "nvidia-compute-utils-G07", "nvidia-gl-G07", "nvidia-video-G07", "nvidia-modprobe", "nvidia-persistenced"} {
		rows += `<solvable type="package" name="` + name + `" edition="` + NvidiaVersion + `-1" arch="x86_64" repository="lyra-nvidia-upstream"/>`
	}
	rows += `<solvable type="package" name="` + NvidiaKMP + `" edition="` + kmpEdition + `160100.2.4" arch="x86_64" repository="lyra-nvidia-oss"/>`
	rows += extraRows
	count := 10 + strings.Count(extraRows, "<solvable ")
	return `<stream><install-summary packages-to-change="` + strconv.Itoa(count) + `"><to-install>` + rows + `</to-install></install-summary>` + commitPrompt + `</stream>`
}

func TestNvidiaFreshBundlePlanWithRuntimeLibraries(t *testing.T) {
	var out bytes.Buffer
	if err := answerNvidiaInstall(strings.NewReader(freshBundlePlanWith(testKMPEdition, runtimeLibraryRows)), &out, testKMPEdition, func() error { return nil }, func(uint32, string) {}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "y\n" {
		t.Fatal(out.String())
	}
	for _, tc := range []struct{ name, rows string }{
		{"untrusted-source", strings.Replace(runtimeLibraryRows, `repository="lyra-nvidia-oss"`, `repository="repo-packman"`, 1)},
		{"foreign-arch", strings.Replace(runtimeLibraryRows, `name="libOpenCL1" edition="2.3.1-160100.2.1" arch="x86_64"`, `name="libOpenCL1" edition="2.3.1-160100.2.1" arch="i586"`, 1)},
		{"other-library", runtimeLibraryRows + `<solvable type="package" name="libvdpau1" edition="1.5-1.1" arch="x86_64" repository="lyra-nvidia-oss"/>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := answerNvidiaInstall(strings.NewReader(freshBundlePlanWith(testKMPEdition, tc.rows)), &out, testKMPEdition, func() error {
				t.Fatal("recovery prepared for a refused plan")
				return nil
			}, func(uint32, string) {})
			if err == nil || out.Len() != 0 {
				t.Fatalf("err=%v reply=%q", err, out.String())
			}
		})
	}
}

func TestNvidiaFreshBundlePlan(t *testing.T) {
	var out bytes.Buffer
	if err := answerNvidiaInstall(strings.NewReader(freshBundlePlan(testKMPEdition)), &out, testKMPEdition, func() error { return nil }, func(uint32, string) {}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "y\n" {
		t.Fatal(out.String())
	}
}

// The KMP must be the one built for the kernel that will boot; a module for
// another release, another driver version or an unqualified flavor is refused
// before the commit prompt is answered.
func TestNvidiaBundlePlanBindsKMPToRunningKernel(t *testing.T) {
	for _, tc := range []struct{ name, planKMP string }{
		{"other-kernel", NvidiaVersion + "_k6.12.0_160100.4-"},
		{"other-driver", "620.10.01_k6.12.0_160100.7-"},
		{"unversioned", NvidiaVersion + "-"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := answerNvidiaInstall(strings.NewReader(freshBundlePlan(tc.planKMP)), &out, testKMPEdition, func() error {
				t.Fatal("recovery prepared for a refused plan")
				return nil
			}, func(uint32, string) {})
			if err == nil || !strings.Contains(err.Error(), NvidiaKMP) || out.Len() != 0 {
				t.Fatalf("err=%v reply=%q", err, out.String())
			}
		})
	}
}

func TestNvidiaKMPEditionFollowsKernelRelease(t *testing.T) {
	for kernel, want := range map[string]string{
		"6.12.0-160100.7-default":  NvidiaVersion + "_k6.12.0_160100.7-",
		"6.12.0-160100.12-default": NvidiaVersion + "_k6.12.0_160100.12-",
	} {
		got, err := NvidiaKMPEdition(kernel)
		if err != nil || got != want {
			t.Fatalf("NvidiaKMPEdition(%q)=%q, %v; want %q", kernel, got, err, want)
		}
	}
	for _, kernel := range []string{"6.12.0-160100.7-rt", "6.12.0-160100.7", "-default", "", "6.12.0_160100.7-default"} {
		if got, err := NvidiaKMPEdition(kernel); err == nil {
			t.Fatalf("NvidiaKMPEdition(%q)=%q accepted an unqualified flavor", kernel, got)
		}
	}
}

func TestNvidiaUnresolvedPlanNamesMissingKMP(t *testing.T) {
	var out bytes.Buffer
	body := `<stream><prompt id="1"><option value="1"/><option value="2"/></prompt></stream>`
	err := answerNvidiaInstall(strings.NewReader(body), &out, testKMPEdition, func() error { return nil }, func(uint32, string) {})
	if err == nil || !strings.Contains(err.Error(), testKMPEdition) || out.Len() != 0 {
		t.Fatalf("err=%v reply=%q", err, out.String())
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
	kernel, err := exec.Command("uname", "-r").Output()
	if err != nil {
		t.Fatal(err)
	}
	kmpEdition, err := NvidiaKMPEdition(strings.TrimSpace(string(kernel)))
	if err != nil {
		t.Fatal(err)
	}
	err = answerNvidiaInstall(io.TeeReader(output, log), input, kmpEdition, func() error {
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

// After the private transaction commits, the enabled system repositories must
// get their metadata cache from the privileged side: lyra-nvidia (OBS) and the
// package-owned upstream channel, never the installer-only OSS mirror whose
// system counterpart already exists.
func TestNvidiaPostInstallRefreshCoversEnabledSystemRepositories(t *testing.T) {
	var got []string
	err := refreshNvidiaRepositoriesWith(func(name string, args ...string) (string, error) {
		got = append([]string{name}, args...)
		return "Repository 'lyra-nvidia' is up to date.", nil
	})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	want := []string{"zypper", "--non-interactive", "refresh", "--", "lyra-nvidia", "lyra-nvidia-upstream"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("refresh command = %q, want %q", got, want)
	}
	err = refreshNvidiaRepositoriesWith(func(string, ...string) (string, error) {
		return "Download (curl) error for 'https://developer.download.nvidia.com/...':\nError code: Connection failed\nError message: Could not resolve host: developer.download.nvidia.com", exec.Command("/bin/sh", "-c", "exit 4").Run()
	})
	var refresh *RepositoryRefreshError
	if !errors.As(err, &refresh) || refresh.Kind != "network" {
		t.Fatalf("network failure = %v, want a network RepositoryRefreshError", err)
	}
}
