package distro

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lyraos/vegad/internal/nvidiarecovery"
)

// This opt-in test binary belongs only in a disposable, networkless VM. It
// exercises the real Zypper prompt/lock, recovery and signed RPM scriptlets.
// Fixture repo URLs are local; production source URLs remain immutable.
func TestGuestExt4NvidiaRecovery(t *testing.T) {
	if os.Getenv("LYRA_NVIDIA_RECOVERY_GUEST") != "1" {
		t.Skip("disposable ext4 guest only")
	}
	cmdline, _ := os.ReadFile("/proc/cmdline")
	serial, _ := os.ReadFile("/sys/block/vda/serial")
	if os.Geteuid() != 0 || !strings.Contains(string(cmdline), "lyra.nvidia-test=1") || strings.TrimSpace(string(serial)) != "lyra-nvidia-test-onl" {
		t.Fatal("not the marked disposable guest")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := nvidiaCommand(ctx, "--xmlout", "--reposd-dir", "/fixtures/repos", "--cache-dir", "/fixtures/cache", "--no-refresh", "install", "--no-recommends", "--download-in-advance", "--", "lyra-nvidia="+NvidiaVersion)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var point *nvidiarecovery.Point
	err = answerNvidiaInstall(out, in, func() error {
		point, err = nvidiarecovery.Prepare(func() { t.Log("verifying recovery") })
		if err != nil {
			return err
		}
		return nvidiarecovery.WriteRecord(nvidiarecovery.Base, nvidiarecovery.Record{Kind: "restic-offline", Reference: point.Manifest.Reference, State: "ready"})
	}, func(percent uint32, message string) { t.Log(percent, message) })
	in.Close()
	if err != nil {
		cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	if point == nil {
		t.Fatal("Zypper committed without the recovery callback")
	}
	if err = point.Mark("installed"); err != nil {
		t.Fatal(err)
	}
	if err = nvidiarecovery.WriteRecord(nvidiarecovery.Base, nvidiarecovery.Record{Kind: "restic-offline", Reference: point.Manifest.Reference, State: "installed"}); err != nil {
		t.Fatal(err)
	}
	t.Log("recovery reference", point.Manifest.Reference)
}
