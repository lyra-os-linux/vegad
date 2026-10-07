package parental

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSupervisedFlatpakLaunchPeerInDisposableVM exercises kernel-provided
// credentials from two distinct SELinux domains. It does not authenticate a
// graphical login, consult policy, resolve a Flatpak ref, or launch an app.
func TestSupervisedFlatpakLaunchPeerInDisposableVM(t *testing.T) {
	if os.Getenv("LYRA_TEST_SUPERVISED_PEER") != "1" {
		t.Skip("explicit disposable VM fixture only")
	}
	boot, err := os.ReadFile("/proc/cmdline")
	if err != nil || !strings.Contains(" "+string(boot)+" ", " lyra.virtualization-test=1 ") {
		t.Fatal("VM marker missing")
	}
	serial, err := os.ReadFile("/sys/block/vda/serial")
	if err != nil || strings.TrimSpace(string(serial)) != "lyra-admission-test" || os.Geteuid() != 0 {
		t.Fatal("unexpected VM or privilege")
	}
	enforce, err := os.ReadFile("/sys/fs/selinux/enforce")
	if err != nil || strings.TrimSpace(string(enforce)) != "1" {
		t.Fatal("SELinux must be Enforcing")
	}

	for _, account := range []struct {
		name, suffix, uid string
	}{
		{"parentaltest", "a", "1003"},
		{"parentaltest2", "b", "1011"},
	} {
		for _, mode := range []string{"valid", "spoof", "fork"} {
			t.Run(account.suffix+"-"+mode, func(t *testing.T) {
				identity, err := user.Lookup(account.name)
				if err != nil || identity.Uid != account.uid {
					t.Fatalf("fixture account changed: %+v %v", identity, err)
				}
				uid, _ := strconv.ParseUint(identity.Uid, 10, 32)
				gid, _ := strconv.ParseUint(identity.Gid, 10, 32)
				path := filepath.Join("/opt/lyra-parental-probe", fmt.Sprintf("peer-%s-%s.sock", account.suffix, mode))
				defer os.Remove(path)
				listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				if err := os.Chmod(path, 0666); err != nil {
					t.Fatal(err)
				}
				if out, err := exec.Command("/usr/bin/chcon", "-t", "lyra_test_peer_sock_t", path).CombinedOutput(); err != nil {
					t.Fatalf("label socket: %v %s", err, out)
				}
				if err := listener.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				context := fmt.Sprintf("system_u:system_r:lyra_test_child_%s_t:s0", account.suffix)
				client := exec.Command("/usr/sbin/runuser", "-u", account.name, "--",
					"/usr/bin/runcon", context,
					"/opt/lyra-parental-probe/peer-client-"+account.suffix, path, mode)
				var output bytes.Buffer
				client.Stdout, client.Stderr = &output, &output
				if err := client.Start(); err != nil {
					t.Fatal(err)
				}
				conn, acceptErr := listener.AcceptUnix()
				if acceptErr == nil {
					defer conn.Close()
				}
				var observed FlatpakLaunchPeer
				var observeErr error
				var peer FlatpakLaunchPeer
				var request FlatpakLaunchRequest
				var readErr error
				if acceptErr == nil {
					observed, observeErr = ObserveFlatpakLaunchPeer(conn)
					peer, request, readErr = ReadFlatpakLaunchRequest(conn)
				}
				waitErr := client.Wait()
				if acceptErr != nil || waitErr != nil {
					t.Fatalf("client did not connect: accept=%v wait=%v output=%s", acceptErr, waitErr, output.String())
				}
				if observeErr != nil || observed.UID != uint32(uid) || observed.GID != uint32(gid) ||
					observed.PID <= 0 || observed.SELinuxContext != context {
					t.Fatalf("peer mismatch: %+v %v", observed, observeErr)
				}
				if mode == "spoof" {
					if !errors.Is(readErr, InvalidFlatpakLaunchRequest) {
						t.Fatalf("spoof accepted: %+v %+v %v", peer, request, readErr)
					}
				} else if mode == "fork" {
					if !errors.Is(readErr, InvalidFlatpakLaunchRequest) ||
						!strings.Contains(output.String(), fmt.Sprintf("connected-pid=%d sender-pid=", observed.PID)) {
						t.Fatalf("transferred socket accepted or fixture invalid: %+v %+v %v output=%s", peer, request, readErr, output.String())
					}
				} else if readErr != nil || peer != observed || request.AppID != "org.lyra.TestApp" {
					t.Fatalf("valid request failed: %+v %+v %v", peer, request, readErr)
				}
				t.Logf("UID=%d SELinux=%s mode=%s", observed.UID, observed.SELinuxContext, mode)
			})
		}
	}
}
