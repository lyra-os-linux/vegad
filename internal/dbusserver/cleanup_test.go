package dbusserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lyraos/vegad/internal/cleanup"
)

type cleanupRecorder struct {
	mu      sync.Mutex
	signals [][]interface{}
	done    chan struct{}
}

func (r *cleanupRecorder) record(signal string, args ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.signals = append(r.signals, append([]interface{}{signal}, args...))
	if signal == "Finished" {
		close(r.done)
	}
}

func newTestCleanup(t *testing.T, clean func(string) (uint64, string, error)) (*CleanupService, *cleanupRecorder) {
	t.Helper()
	saved := operations
	operations = newTestGate(false)
	t.Cleanup(func() { operations = saved })
	recorder := &cleanupRecorder{done: make(chan struct{})}
	s := newCleanupService(&Activity{last: time.Now()}, nil)
	s.newEnv = func() *cleanup.Env { return &cleanup.Env{} }
	s.clean = func(_ context.Context, _ *cleanup.Env, id string) (uint64, string, error) { return clean(id) }
	s.onEmit = recorder.record
	return s, recorder
}

func TestCleanRunsCategoriesInOrderAndSumsFreedBytes(t *testing.T) {
	s, recorder := newTestCleanup(t, func(id string) (uint64, string, error) {
		if id == "coredumps" {
			return 210 << 20, cleanup.ResultOK, nil
		}
		return 0, cleanup.ResultSkipped, nil
	})
	job, err := s.start([]string{"coredumps", "package-cache"})
	if err != nil {
		t.Fatal(err)
	}
	<-recorder.done
	var finished []interface{}
	var order []string
	for _, signal := range recorder.signals {
		switch signal[0] {
		case "CategoryFinished":
			order = append(order, signal[2].(string)+":"+signal[3].(string))
		case "Finished":
			finished = signal
		}
	}
	if len(order) != 2 || order[0] != "coredumps:ok" || order[1] != "package-cache:skipped" {
		t.Fatalf("%v", order)
	}
	if finished[1] != job || finished[2] != "ok" || finished[3] != uint64(210<<20) {
		t.Fatalf("%v", finished)
	}
}

// vegad#127: Progress goes 0% for the first category, then by category,
// and 100% before Finished, always for the job and a requested category.
func TestCleanEmitsProgressPerCategoryAndAtTheEnd(t *testing.T) {
	s, recorder := newTestCleanup(t, func(string) (uint64, string, error) { return 0, cleanup.ResultSkipped, nil })
	job, err := s.start([]string{"coredumps", "journal", "package-cache", "snapshots"})
	if err != nil {
		t.Fatal(err)
	}
	<-recorder.done
	var progress []string
	for i, signal := range recorder.signals {
		if signal[0] != "Progress" {
			continue
		}
		if signal[1] != job {
			t.Fatalf("Progress for job %v, want %v", signal[1], job)
		}
		progress = append(progress, fmt.Sprintf("%s:%d", signal[2], signal[3]))
		if signal[3] == uint32(100) && recorder.signals[i+1][0] != "Finished" {
			t.Fatalf("100%% is not the signal before Finished: %v", recorder.signals[i+1])
		}
	}
	want := []string{"coredumps:0", "journal:25", "package-cache:50", "snapshots:75", "snapshots:100"}
	if strings.Join(progress, " ") != strings.Join(want, " ") {
		t.Fatalf("progress %v, want %v", progress, want)
	}
}

func TestCleanReportsPartialAndFailedJobs(t *testing.T) {
	s, recorder := newTestCleanup(t, func(id string) (uint64, string, error) {
		if id == "journal" {
			return 0, cleanup.ResultFailed, errors.New("falhou")
		}
		return 1, cleanup.ResultOK, nil
	})
	if _, err := s.start([]string{"coredumps", "journal"}); err != nil {
		t.Fatal(err)
	}
	<-recorder.done
	last := recorder.signals[len(recorder.signals)-1]
	if last[0] != "Finished" || last[2] != "partial" {
		t.Fatalf("%v", last)
	}
}

func TestCleanRejectsUnknownEmptyAndRepeatedIDs(t *testing.T) {
	s, _ := newTestCleanup(t, func(string) (uint64, string, error) { t.Fatal("não deveria limpar"); return 0, "", nil })
	for _, ids := range [][]string{nil, {"Documentos"}, {"coredumps", "coredumps"}} {
		if _, err := s.start(ids); err == nil || err.Name != BusName+".Error.NotSupported" {
			t.Fatalf("%v: %v", ids, err)
		}
	}
}

func TestCleanIsBusyDuringATransaction(t *testing.T) {
	s, _ := newTestCleanup(t, func(string) (uint64, string, error) { t.Fatal("limpou durante o UpdateAll"); return 0, "", nil })
	release, err := operations.beginTransaction()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := s.start([]string{"package-cache"}); err == nil || err.Name != BusName+".Error.Busy" {
		t.Fatalf("%v", err)
	}
}
