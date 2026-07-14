package dbusserver

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
)

// ShutdownInhibitor holds a systemd-logind delay lock (see logind's
// org.freedesktop.login1.Manager.Inhibit, mode "delay") for the duration of
// a mutating operation — package transaction, backup, restore — to give
// logind a short, explicit grace window during shutdown/reboot.
//
// vegad itself has no SIGTERM handling: without this lock, systemd simply
// SIGTERMs the unit's whole cgroup on shutdown, which can kill zypper/rpm or
// restic mid-write. The lock does not guarantee completion of long jobs:
// logind caps delay inhibitors at InhibitDelayMaxSec and systemd may still
// send SIGTERM after that window.
type ShutdownInhibitor struct {
	conn *dbus.Conn
	fd   dbus.UnixFD
	once sync.Once
}

const inhibitorReasonLimit = 128

func sanitizeInhibitorReason(why string) string {
	var b strings.Builder
	for _, r := range why {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > inhibitorReasonLimit {
			break
		}
		b.WriteRune(r)
	}
	value := strings.TrimSpace(b.String())
	if value == "" {
		return "operação do Vega"
	}
	return value
}

// acquireShutdownInhibit takes a "delay" inhibitor lock for "shutdown" and
// "sleep", tagged with why. Call Release when the operation finishes,
// succeeds or fails — an unreleased lock only holds up shutdown until
// logind's InhibitDelayMaxSec elapses, but should still always be released
// promptly.
func acquireShutdownInhibit(why string) (*ShutdownInhibitor, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("inhibit: connect to system bus: %w", err)
	}

	obj := conn.Object("org.freedesktop.login1", dbus.ObjectPath("/org/freedesktop/login1"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var fd dbus.UnixFD
	err = obj.CallWithContext(ctx, "org.freedesktop.login1.Manager.Inhibit", 0,
		"shutdown:sleep", "Vega", sanitizeInhibitorReason(why), "delay").Store(&fd)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("inhibit: Manager.Inhibit: %w", err)
	}

	return &ShutdownInhibitor{conn: conn, fd: fd}, nil
}

// Release drops the inhibitor lock. Safe to call on a nil receiver so
// callers can unconditionally `defer inhibitor.Release()` even when
// acquireShutdownInhibit failed (logged, not fatal — see withShutdownInhibit).
func (i *ShutdownInhibitor) Release() {
	if i == nil {
		return
	}
	i.once.Do(func() {
		if err := syscall.Close(int(i.fd)); err != nil {
			log.Printf("vegad: release shutdown inhibitor: close fd: %v", err)
		}
		_ = i.conn.Close()
	})
}

// withShutdownInhibit runs work while holding a shutdown/sleep delay
// inhibitor. Failure to acquire the lock is logged but doesn't block work —
// a slightly-less-safe shutdown beats refusing to run the operation at all.
func withShutdownInhibit(why string, work func() error) error {
	inhibitor, err := acquireShutdownInhibit(why)
	if err != nil {
		log.Printf("vegad: could not acquire shutdown inhibitor (%s): %v", why, err)
	}
	defer inhibitor.Release()
	return work()
}

// WithShutdownInhibit is withShutdownInhibit exported for cmd/vegad's
// standalone "backup run" subprocess, which lives outside this package.
func WithShutdownInhibit(why string, work func() error) error {
	return withShutdownInhibit(why, work)
}
