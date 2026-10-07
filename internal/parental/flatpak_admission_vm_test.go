package parental

import (
	"encoding/binary"
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

func TestFlatpakLaunchPeerInDisposableVM(t *testing.T) {
	boot, err := os.ReadFile("/proc/cmdline")
	if err != nil || !strings.Contains(" "+string(boot)+" ", " lyra.virtualization-test=1 ") {
		t.Skip("disposable VM only")
	}
	serial, err := os.ReadFile("/sys/block/vda/serial")
	if err != nil || strings.TrimSpace(string(serial)) != "lyra-admission-test" || os.Geteuid() != 0 {
		t.Fatal("unexpected VM or privilege")
	}
	enforce, err := os.ReadFile("/sys/fs/selinux/enforce")
	if err != nil || strings.TrimSpace(string(enforce)) != "1" {
		t.Fatal("SELinux must be Enforcing")
	}
	account, err := user.Lookup("parentaltest")
	if err != nil || account.Uid != "1003" {
		t.Fatalf("unexpected fixture account: %+v %v", account, err)
	}
	accountGID, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil {
		t.Fatal(err)
	}

	for name, payload := range map[string]string{
		"valid": `{"app_id":"org.lyra.TestApp"}`,
		"spoof": `{"app_id":"org.lyra.TestApp","uid":0}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("/tmp", fmt.Sprintf("lyra-flatpak-peer-%d-%s.sock", os.Getpid(), name))
			defer os.Remove(path)
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := os.Chmod(path, 0666); err != nil {
				t.Fatal(err)
			}
			if err := listener.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}

			frame := make([]byte, len(payload)+2)
			binary.BigEndian.PutUint16(frame, uint16(len(payload)))
			copy(frame[2:], payload)
			// setpriv changes the Unix UID without opening a PAM session: this
			// isolates SO_PEERCRED/SO_PEERSEC, not login integration.
			client := exec.Command("/usr/bin/setpriv", "--reuid=1003", "--regid="+account.Gid, "--clear-groups",
				"/usr/bin/python3", "-c",
				"import socket,sys; s=socket.socket(socket.AF_UNIX); s.connect(sys.argv[1]); s.sendall(bytes.fromhex(sys.argv[2])); s.close()",
				path, fmt.Sprintf("%x", frame))
			if err := client.Start(); err != nil {
				t.Fatal(err)
			}
			conn, err := listener.AcceptUnix()
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			peer, request, readErr := ReadFlatpakLaunchRequest(conn)
			if err := client.Wait(); err != nil {
				t.Fatal(err)
			}
			if name == "spoof" {
				if !errors.Is(readErr, InvalidFlatpakLaunchRequest) {
					t.Fatalf("claimed UID was accepted: %+v %+v %v", peer, request, readErr)
				}
				return
			}
			if readErr != nil || request.AppID != "org.lyra.TestApp" || peer.UID != 1003 ||
				peer.GID != uint32(accountGID) || peer.PID <= 0 || peer.SELinuxContext == "" ||
				peer.SELinuxContext == "kernel" {
				t.Fatalf("kernel peer/request mismatch: %+v %+v %v", peer, request, readErr)
			}
			t.Logf("kernel UID=%s SELinux=%s", strconv.FormatUint(uint64(peer.UID), 10), peer.SELinuxContext)
		})
	}
}
