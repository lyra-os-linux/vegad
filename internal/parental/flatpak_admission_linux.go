package parental

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const InvalidFlatpakLaunchRequest Code = "InvalidFlatpakLaunchRequest"
const UnavailableFlatpakPeer Code = "UnavailableFlatpakPeer"

const maxFlatpakLaunchFrame = 512

// The first prototype accepts only app IDs, not refs, paths, arguments,
// environment, UIDs, or file descriptors supplied by the caller. The resolver
// and policy authority must determine the full ref and deployment separately.
var launchAppID = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)+\.[A-Za-z_][A-Za-z0-9_-]*$`)

type FlatpakLaunchRequest struct {
	AppID string
}

// FlatpakLaunchPeer is an observation made by the kernel at connection time.
// It is NOT an authorization: the broker must still bind UID/GID and the exact
// SELinux context to a current supervised account and policy generation.
type FlatpakLaunchPeer struct {
	PID            int32
	UID, GID       uint32
	SELinuxContext string
}

// ParseFlatpakLaunchFrame accepts exactly one length-prefixed JSON object.
// Duplicate fields, claimed credentials, alternate refs and trailing bytes
// fail closed. Frame length is a 16-bit big-endian integer.
func ParseFlatpakLaunchFrame(frame []byte) (FlatpakLaunchRequest, error) {
	if len(frame) < 3 {
		return FlatpakLaunchRequest{}, InvalidFlatpakLaunchRequest
	}
	length := int(binary.BigEndian.Uint16(frame[:2]))
	if length == 0 || length > maxFlatpakLaunchFrame || len(frame) != length+2 {
		return FlatpakLaunchRequest{}, InvalidFlatpakLaunchRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(frame[2:]))
	decoder.UseNumber()
	value, err := readJSON(decoder, 0)
	if err != nil {
		return FlatpakLaunchRequest{}, InvalidFlatpakLaunchRequest
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return FlatpakLaunchRequest{}, InvalidFlatpakLaunchRequest
	}
	row, ok := object(value, "app_id")
	if !ok {
		return FlatpakLaunchRequest{}, InvalidFlatpakLaunchRequest
	}
	appID, ok := row["app_id"].(string)
	if !ok || len(appID) > 255 || !launchAppID.MatchString(appID) {
		return FlatpakLaunchRequest{}, InvalidFlatpakLaunchRequest
	}
	return FlatpakLaunchRequest{AppID: appID}, nil
}

// ObserveFlatpakLaunchPeer reads SO_PEERCRED and SO_PEERSEC from the accepted
// Unix socket. A missing/placeholder SELinux label is a denial, never a
// fallback to an unconfined account. This routine does not launch an app.
func ObserveFlatpakLaunchPeer(conn *net.UnixConn) (FlatpakLaunchPeer, error) {
	if conn == nil {
		return FlatpakLaunchPeer{}, UnavailableFlatpakPeer
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return FlatpakLaunchPeer{}, UnavailableFlatpakPeer
	}
	var peer FlatpakLaunchPeer
	var socketErr error
	if err = raw.Control(func(fd uintptr) {
		credentials, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if e != nil {
			socketErr = e
			return
		}
		context, e := unix.GetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_PEERSEC)
		if e != nil {
			socketErr = e
			return
		}
		peer = FlatpakLaunchPeer{PID: credentials.Pid, UID: credentials.Uid,
			GID: credentials.Gid, SELinuxContext: strings.TrimRight(context, "\x00")}
	}); err != nil || socketErr != nil || peer.PID <= 0 || peer.UID == 0 ||
		peer.SELinuxContext == "" || peer.SELinuxContext == "kernel" {
		return FlatpakLaunchPeer{}, UnavailableFlatpakPeer
	}
	return peer, nil
}

// ReadFlatpakLaunchRequest has one bounded read per connection. The caller
// must authenticate the observed peer against the live parental registry and
// resolve the app to a pinned deployment before any process can be started.
func ReadFlatpakLaunchRequest(conn *net.UnixConn) (FlatpakLaunchPeer, FlatpakLaunchRequest, error) {
	peer, err := ObserveFlatpakLaunchPeer(conn)
	if err != nil {
		return FlatpakLaunchPeer{}, FlatpakLaunchRequest{}, err
	}
	if err = conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return FlatpakLaunchPeer{}, FlatpakLaunchRequest{}, InvalidFlatpakLaunchRequest
	}
	defer conn.SetReadDeadline(time.Time{})
	var header [2]byte
	if _, err = io.ReadFull(conn, header[:]); err != nil {
		return FlatpakLaunchPeer{}, FlatpakLaunchRequest{}, InvalidFlatpakLaunchRequest
	}
	length := int(binary.BigEndian.Uint16(header[:]))
	if length == 0 || length > maxFlatpakLaunchFrame {
		return FlatpakLaunchPeer{}, FlatpakLaunchRequest{}, InvalidFlatpakLaunchRequest
	}
	frame := make([]byte, length+2)
	copy(frame, header[:])
	if _, err = io.ReadFull(conn, frame[2:]); err != nil {
		return FlatpakLaunchPeer{}, FlatpakLaunchRequest{}, InvalidFlatpakLaunchRequest
	}
	request, err := ParseFlatpakLaunchFrame(frame)
	if err != nil {
		return FlatpakLaunchPeer{}, FlatpakLaunchRequest{}, err
	}
	return peer, request, nil
}
