package dbusserver

import (
	"fmt"
	"log"
	"os/user"
	"strconv"

	"github.com/godbus/dbus/v5"
)

// desktopUser identifies the flesh-and-blood account behind a D-Bus caller.
// vegad runs as root (see packaging/vegad/vegad.service), so without this a
// `flatpak --user` call would operate on root's own — irrelevant — per-user
// Flatpak installation instead of the desktop user's.
type desktopUser struct {
	Uid        uint32
	Gid        uint32
	Username   string
	HomeDir    string
	RuntimeDir string
}

// resolveDesktopUser asks the bus daemon itself (org.freedesktop.DBus,
// GetConnectionUnixUser) which UID owns the connection behind sender — this
// is enforced by the bus daemon via the socket's peer credentials, not
// something a caller can spoof. Returns (nil, nil) for a uid-0 sender (e.g.
// another root process): root has no separate desktop scope to query.
func resolveDesktopUser(conn *dbus.Conn, sender dbus.Sender) (*desktopUser, error) {
	if sender == "" {
		return nil, fmt.Errorf("chamada sem remetente D-Bus")
	}

	var uid uint32
	if err := conn.BusObject().Call("org.freedesktop.DBus.GetConnectionUnixUser", 0, string(sender)).Store(&uid); err != nil {
		return nil, fmt.Errorf("resolver uid do remetente: %w", err)
	}
	if uid == 0 {
		return nil, nil
	}

	entry, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return nil, fmt.Errorf("consultar usuário %d: %w", uid, err)
	}
	gid, err := strconv.ParseUint(entry.Gid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("gid inválido para %s: %w", entry.Username, err)
	}

	return &desktopUser{
		Uid:        uid,
		Gid:        uint32(gid),
		Username:   entry.Username,
		HomeDir:    entry.HomeDir,
		RuntimeDir: fmt.Sprintf("/run/user/%d", uid),
	}, nil
}

// desktopUserOrNil wraps resolveDesktopUser for the read-only/aggregate
// Software paths, where a resolution failure (unusual bus setup, caller
// already gone, no passwd entry) should degrade to system-scope-only Flatpak
// results instead of failing the whole request.
func desktopUserOrNil(conn *dbus.Conn, sender dbus.Sender, logCtx string) *desktopUser {
	u, err := resolveDesktopUser(conn, sender)
	if err != nil {
		log.Printf("vegad: %s: não foi possível resolver o usuário de desktop, ignorando o escopo --user do Flatpak: %v", logCtx, err)
		return nil
	}
	return u
}
