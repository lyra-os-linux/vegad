package dbusserver

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestGate(zypper bool) *operationGate {
	return &operationGate{zypperLocked: func() bool { return zypper }}
}

func TestDiagnosticExcludesTransactionsBothWays(t *testing.T) {
	gate := newTestGate(false)
	releaseTx, err := gate.beginTransaction()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.beginDiagnostic(); err == nil || err.Name != BusName+".Error.Busy" {
		t.Fatalf("diagnostic during a transaction: %v", err)
	}
	releaseTx()
	releaseTx() // idempotent: a second release must not unbalance the count

	releaseDiag, err := gate.beginDiagnostic()
	if err != nil {
		t.Fatalf("diagnostic after the transaction: %v", err)
	}
	if _, err := gate.beginTransaction(); err == nil || err.Name != BusName+".Error.Busy" {
		t.Fatalf("transaction during a diagnostic: %v", err)
	}
	if _, err := gate.beginDiagnostic(); err == nil {
		t.Fatal("two diagnostics at once")
	}
	releaseDiag()
	if _, err := gate.beginTransaction(); err != nil {
		t.Fatalf("transaction after the diagnostic: %v", err)
	}
}

func TestCleanupExcludesDiagnosticsAndTransactions(t *testing.T) {
	gate := newTestGate(false)
	release, err := gate.beginCleanup()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.beginTransaction(); err == nil || err.Name != BusName+".Error.Busy" {
		t.Fatalf("transaction during a cleanup: %v", err)
	}
	if _, err := gate.beginDiagnostic(); err == nil {
		t.Fatal("diagnostic during a cleanup")
	}
	if _, err := gate.beginCleanup(); err == nil {
		t.Fatal("two cleanups at once")
	}
	release()
	releaseTx, err := gate.beginTransaction()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.beginCleanup(); err == nil || err.Name != BusName+".Error.Busy" {
		t.Fatalf("cleanup during an UpdateAll: %v", err)
	}
	releaseTx()
	if _, err := newTestGate(true).beginCleanup(); err == nil {
		t.Fatal("cleanup with zypper locked elsewhere")
	}
}

func TestTransactionsDoNotExcludeEachOther(t *testing.T) {
	gate := newTestGate(false)
	first, err := gate.beginTransaction()
	if err != nil {
		t.Fatal(err)
	}
	second, err := gate.beginTransaction()
	if err != nil {
		t.Fatal(err)
	}
	first()
	if _, err := gate.beginDiagnostic(); err == nil {
		t.Fatal("diagnostic while one of two transactions still runs")
	}
	second()
	if _, err := gate.beginDiagnostic(); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticRefusesExternalZypper(t *testing.T) {
	if _, err := newTestGate(true).beginDiagnostic(); err == nil || err.Name != BusName+".Error.Busy" {
		t.Fatalf("diagnostic with zypper locked elsewhere: %v", err)
	}
}

func TestSoftwareTransactionReportsBusyDuringDiagnostic(t *testing.T) {
	saved := operations
	operations = newTestGate(false)
	defer func() { operations = saved }()
	release, err := operations.beginDiagnostic()
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	s := &SoftwareService{activity: &Activity{last: time.Now()}}
	ran := false
	id, busy := s.startTransaction("fixture", func(progressFunc, packageProgressFunc) error { ran = true; return nil })
	if id != 0 || busy == nil || busy.Name != BusName+".Error.Busy" {
		t.Fatalf("id = %d, err = %v", id, busy)
	}
	b := &BackupService{activity: &Activity{last: time.Now()}}
	noop := func(uint32, uint32, string) error { return nil }
	if _, busy := b.startTransaction("fixture", noop, func(uint32, bool, string) error { return nil }, func(progressFunc) error { ran = true; return nil }); busy == nil {
		t.Fatal("backup started during a diagnostic")
	}
	time.Sleep(10 * time.Millisecond)
	if ran {
		t.Fatal("refused work ran anyway")
	}
}

func TestZyppLockOwnerMustBeAlive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zypp.pid")
	alive := func(pid int) bool { return pid == 4242 }
	if zyppLockOwnerAlive(path, alive) {
		t.Fatal("missing lock file reported as held")
	}
	for content, want := range map[string]bool{"4242\n": true, "17\n": false, "garbage": false, "": false} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := zyppLockOwnerAlive(path, alive); got != want {
			t.Errorf("lock %q: got %v, want %v", content, got, want)
		}
	}
}
