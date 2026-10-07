package parental

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func flatpakFrame(payload string) []byte {
	frame := make([]byte, len(payload)+2)
	binary.BigEndian.PutUint16(frame, uint16(len(payload)))
	copy(frame[2:], payload)
	return frame
}

func TestFlatpakLaunchFrameRejectsClientAuthority(t *testing.T) {
	valid := flatpakFrame(`{"app_id":"org.lyra.TestApp"}`)
	request, err := ParseFlatpakLaunchFrame(valid)
	if err != nil || request.AppID != "org.lyra.TestApp" {
		t.Fatalf("valid app request: %+v %v", request, err)
	}
	for name, payload := range map[string]string{
		"claimed-uid":       `{"app_id":"org.lyra.TestApp","uid":1003}`,
		"claimed-context":   `{"app_id":"org.lyra.TestApp","selinux_context":"trusted"}`,
		"free-arguments":    `{"app_id":"org.lyra.TestApp","argv":["--app-path=/tmp/evil"]}`,
		"alternate-ref":     `{"app_id":"org.lyra.TestApp","ref":"app/org.lyra.TestApp/x86_64/stable"}`,
		"duplicate-id":      `{"app_id":"org.lyra.TestApp","app_id":"org.lyra.Other"}`,
		"null-id":           `{"app_id":null}`,
		"flag-as-id":        `{"app_id":"--app-path=/tmp/evil"}`,
		"path-as-id":        `{"app_id":"/tmp/org.lyra.TestApp"}`,
		"unicode-lookalike": `{"app_id":"org.lyra.Tést"}`,
		"extra-object":      `{"app_id":"org.lyra.TestApp"}{"app_id":"org.lyra.Other"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseFlatpakLaunchFrame(flatpakFrame(payload)); !errors.Is(err, InvalidFlatpakLaunchRequest) {
				t.Fatalf("accepted %s: %v", payload, err)
			}
		})
	}
	for name, frame := range map[string][]byte{
		"empty":         nil,
		"missing-body":  []byte{0, 9},
		"trailing-byte": append(append([]byte{}, valid...), 0),
		"oversize":      append([]byte{2, 1}, make([]byte, 513)...),
		"bad-length":    append([]byte{0, 1}, valid[2:]...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseFlatpakLaunchFrame(frame); !errors.Is(err, InvalidFlatpakLaunchRequest) {
				t.Fatalf("accepted malformed frame: %v", err)
			}
		})
	}
	if _, err := ParseFlatpakLaunchFrame(flatpakFrame(`{"app_id":"` + strings.Repeat("a", 256) + `"}`)); !errors.Is(err, InvalidFlatpakLaunchRequest) {
		t.Fatalf("accepted overlong app ID: %v", err)
	}
}

func TestFlatpakPeerComesFromKernel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer.sock")
	addr := &net.UnixAddr{Name: path, Net: "unix"}
	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialUnix("unix", nil, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	accepted, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer accepted.Close()
	peer, err := ObserveFlatpakLaunchPeer(accepted)
	if os.Getuid() == 0 || err != nil {
		// Host CI may lack SELinux/SO_PEERSEC; it must deny rather than
		// invent a context. VM tests cover the positive labeled path.
		if !errors.Is(err, UnavailableFlatpakPeer) {
			t.Fatalf("unlabeled/root peer was admitted: %+v %v", peer, err)
		}
		return
	}
	if peer.UID != uint32(os.Getuid()) || peer.GID != uint32(os.Getgid()) || peer.PID != int32(os.Getpid()) {
		t.Fatalf("unexpected kernel peer: %+v", peer)
	}
}
