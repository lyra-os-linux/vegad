package dbusserver

import (
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

// operationGate keeps an exclusive operation (a health diagnostic or a space
// cleanup) and system transactions (Zypper, Flatpak, snapshots, backup,
// kernel) mutually exclusive in both directions: a stress test must not
// compete with an update, an update must not change the system under a
// package or filesystem check, and a cleanup must not remove a cache or a
// kernel a transaction is using. Transactions do not exclude each other here;
// their own locks still apply.
type operationGate struct {
	mu           sync.Mutex
	transactions int
	// exclusive names the running exclusive operation ("" when none).
	exclusive string
	// zypperLocked reports an external Zypper (timer, first update, shell)
	// that vegad did not start. Replaced in tests.
	zypperLocked func() bool
}

var operations = &operationGate{zypperLocked: zypperLockHeld}

func shuttingDownError() *dbus.Error {
	return dbus.NewError(BusName+".Error.ShuttingDown", []interface{}{"daemon encerrando; repita a chamada"})
}

func busyError(detail string) *dbus.Error {
	return dbus.NewError(BusName+".Error.Busy", []interface{}{detail})
}

// beginTransaction registers a system transaction for its whole lifetime.
// The returned release must be called exactly once when the work ends.
func (g *operationGate) beginTransaction() (func(), *dbus.Error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch g.exclusive {
	case exclusiveDiagnostic:
		return nil, busyError("um diagnóstico de saúde está em andamento")
	case exclusiveCleanup:
		return nil, busyError("uma limpeza de espaço está em andamento")
	}
	g.transactions++
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.transactions--
			g.mu.Unlock()
		})
	}, nil
}

const (
	exclusiveDiagnostic = "diagnostic"
	exclusiveCleanup    = "cleanup"
)

// beginDiagnostic admits one diagnostic at a time, and only while no
// transaction or cleanup runs inside or outside vegad.
func (g *operationGate) beginDiagnostic() (func(), *dbus.Error) {
	return g.beginExclusive(exclusiveDiagnostic)
}

// beginCleanup admits a space cleanup under the same rules.
func (g *operationGate) beginCleanup() (func(), *dbus.Error) {
	return g.beginExclusive(exclusiveCleanup)
}

func (g *operationGate) beginExclusive(kind string) (func(), *dbus.Error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case g.exclusive == exclusiveDiagnostic:
		return nil, busyError("um diagnóstico de saúde está em andamento")
	case g.exclusive == exclusiveCleanup:
		return nil, busyError("uma limpeza de espaço está em andamento")
	case g.transactions > 0:
		return nil, busyError("uma operação de software, snapshot ou backup está em andamento")
	case g.zypperLocked != nil && g.zypperLocked():
		return nil, busyError("o Zypper está em uso por outro processo")
	}
	g.exclusive = kind
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.exclusive = ""
			g.mu.Unlock()
		})
	}, nil
}

const zyppLockPath = "/run/zypp.pid"

// zypperLockHeld follows libzypp: the lock file holds the owner's PID and is
// stale when that process no longer exists.
func zypperLockHeld() bool {
	return zyppLockOwnerAlive(zyppLockPath, func(pid int) bool {
		_, err := os.Stat("/proc/" + strconv.Itoa(pid))
		return err == nil
	})
}

func zyppLockOwnerAlive(path string, alive func(int) bool) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return false
	}
	return alive(pid)
}
