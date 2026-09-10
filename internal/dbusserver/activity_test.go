package dbusserver

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func ageActivity(a *Activity) {
	a.mu.Lock()
	a.last = time.Now().Add(-2 * IdleTimeout)
	a.mu.Unlock()
}

func TestActivityWaitsForEveryOperationAndRestartsIdleWindow(t *testing.T) {
	a := &Activity{}
	first, _ := a.begin()
	second, _ := a.begin()
	ageActivity(a)
	if a.stopIfIdle(IdleTimeout) {
		t.Fatal("stopped with two operations active")
	}
	first()
	ageActivity(a)
	if a.stopIfIdle(IdleTimeout) {
		t.Fatal("stopped with one operation active")
	}
	second()
	if a.stopIfIdle(IdleTimeout) {
		t.Fatal("completion did not restart the idle window")
	}
	ageActivity(a)
	if !a.stopIfIdle(IdleTimeout) {
		t.Fatal("idle daemon did not stop")
	}
	if _, ok := a.begin(); ok {
		t.Fatal("new operation started after shutdown committed")
	}
}

func TestActivityStartAndStopAreMutuallyExclusive(t *testing.T) {
	for i := 0; i < 100; i++ {
		a := &Activity{}
		var started, stopped bool
		var done func()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); done, started = a.begin() }()
		go func() { defer wg.Done(); stopped = a.stopIfIdle(IdleTimeout) }()
		wg.Wait()
		if started && stopped {
			t.Fatal("work started on a retiring daemon")
		}
		if started {
			done()
		}
	}
}

type activityTestService struct{ activity *Activity }

func (s *activityTestService) Echo(sender dbus.Sender, value string) (string, *dbus.Error) {
	ageActivity(s.activity)
	if s.activity.stopIfIdle(IdleTimeout) {
		return "", dbus.MakeFailedError(os.ErrClosed)
	}
	return string(sender) + value, nil
}

func TestTrackedMethodsPreserveDBusSignatureAndRejectCallsAfterStop(t *testing.T) {
	a := &Activity{}
	methods := trackedMethods(&activityTestService{a}, a)
	echo, ok := methods["Echo"].(func(dbus.Sender, string) (string, *dbus.Error))
	if !ok {
		t.Fatal("exported method signature changed")
	}
	if result, err := echo(":1.2", "value"); err != nil || result != ":1.2value" {
		t.Fatalf("method not protected or arguments changed: %q %v", result, err)
	}
	ageActivity(a)
	if !a.stopIfIdle(IdleTimeout) {
		t.Fatal("completed method still holds activity")
	}
	if _, err := echo(":1.2", "value"); err == nil || err.Name != BusName+".Error.ShuttingDown" {
		t.Fatalf("queued call after stop = %v", err)
	}
}

func TestTransactionsHoldActivityBeforeReturningID(t *testing.T) {
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "absent-bus"))
	for _, kind := range []string{"software", "repository", "backup"} {
		t.Run(kind, func(t *testing.T) {
			a := &Activity{}
			release, finished := make(chan struct{}), make(chan struct{})
			work := func() error {
				defer close(finished)
				<-release
				// No actual D-Bus signals are emitted; deferred activity release
				// must still run even when work exits without a normal return.
				runtime.Goexit()
				return nil
			}
			s := &SoftwareService{activity: a}
			var id uint32
			switch kind {
			case "software":
				id = s.startTransaction("fixture", func(progressFunc, packageProgressFunc) error { return work() })
			case "repository":
				id = s.startTransactionWithID("fixture", func(uint32, progressFunc) error { return work() })
			case "backup":
				b := &BackupService{activity: a}
				emit := func(uint32, uint32, string) error { return nil }
				id = b.startTransaction("fixture", emit, func(uint32, bool, string) error { return nil }, func(progressFunc) error { return work() })
			}
			ageActivity(a)
			stopped := a.stopIfIdle(IdleTimeout)
			close(release)
			<-finished
			if id == 0 || stopped {
				t.Fatalf("id = %d, stopped before work completed = %v", id, stopped)
			}
		})
	}
}

func TestBackupTransactionSurvivesRealIdleTimeout(t *testing.T) {
	if os.Getenv("VEGA_TEST_LONG_IDLE") != "1" {
		t.Skip("set VEGA_TEST_LONG_IDLE=1 for the 130-second no-polling regression")
	}
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "absent-bus"))
	a := &Activity{last: time.Now()}
	b := &BackupService{activity: a}
	release := make(chan struct{})
	finished := make(chan struct{})
	b.startTransaction("long backup fixture", func(uint32, uint32, string) error { return nil }, func(uint32, bool, string) error { close(finished); return nil }, func(progressFunc) error {
		<-release
		return nil
	})
	server := &Server{activity: a}
	exited := make(chan struct{})
	go func() { server.Run(); close(exited) }()
	select {
	case <-exited:
		close(release)
		t.Fatal("daemon stopped during a transaction without progress or polling")
	case <-time.After(IdleTimeout + 10*time.Second):
	}
	close(release)
	<-finished
	// End the real Run loop without waiting another full idle timeout.
	for {
		a.mu.Lock()
		active := a.active
		if active == 0 {
			a.last = time.Now().Add(-2 * IdleTimeout)
		}
		a.mu.Unlock()
		if active == 0 {
			break
		}
		runtime.Gosched()
	}
	select {
	case <-exited:
	case <-time.After(20 * time.Second):
		t.Fatal("daemon did not retire after the transaction ended")
	}
}
