package dbusserver

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/cleanup"
)

// CleanupCategory is the (stbs) row of Cleanup.Scan.
type CleanupCategory struct {
	Id       string
	Bytes    uint64
	CanClean bool
	Detail   string
}

const cleanupAction = "org.lyraos.vega.storage.cleanup"

// CleanupService implements org.lyraos.Vega1.Cleanup (space-cleanup-v1):
// Scan measures, Clean runs one job at a time under the operation gate.
type CleanupService struct {
	activity *Activity
	conn     *dbus.Conn
	// newEnv builds the host environment; replaced in tests.
	newEnv func() *cleanup.Env
	// clean cleans one category; replaced in tests.
	clean func(ctx context.Context, env *cleanup.Env, id string) (uint64, string, error)
	// onEmit observes signals in tests.
	onEmit func(signal string, args ...interface{})

	mu     sync.Mutex
	lastID uint32
}

func newCleanupService(activity *Activity, conn *dbus.Conn) *CleanupService {
	return &CleanupService{
		activity: activity,
		conn:     conn,
		newEnv:   func() *cleanup.Env { return cleanup.HostEnv(withSnapshots) },
		clean:    cleanup.Clean,
	}
}

// Scan is a silent privileged read: the router checks storage.read-admin.
func (s *CleanupService) Scan() ([]CleanupCategory, *dbus.Error) {
	s.activity.Touch()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	rows := cleanup.Scan(ctx, s.newEnv())
	out := make([]CleanupCategory, 0, len(rows))
	for _, row := range rows {
		out = append(out, CleanupCategory{Id: row.ID, Bytes: row.Bytes, CanClean: row.CanClean, Detail: row.Detail})
	}
	return out, nil
}

func (s *CleanupService) Clean(sender dbus.Sender, ids []string) (uint32, *dbus.Error) {
	s.activity.Touch()
	if err := requirePolkit(sender, cleanupAction); err != nil {
		return 0, err
	}
	return s.start(ids)
}

func (s *CleanupService) start(ids []string) (uint32, *dbus.Error) {
	if len(ids) == 0 {
		return 0, notSupported("nenhuma categoria pedida")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !cleanup.Known(id) {
			return 0, notSupported("categoria desconhecida: " + id)
		}
		if seen[id] {
			return 0, notSupported("categoria repetida: " + id)
		}
		seen[id] = true
	}
	release, busy := operations.beginCleanup()
	if busy != nil {
		return 0, busy
	}
	done, ok := s.activity.begin()
	if !ok {
		release()
		return 0, shuttingDownError()
	}
	s.mu.Lock()
	s.lastID++
	jobID := s.lastID
	s.mu.Unlock()
	env := s.newEnv()
	ids = append([]string{}, ids...)
	go func() {
		defer done()
		defer release()
		s.run(jobID, env, ids)
	}()
	return jobID, nil
}

func (s *CleanupService) run(jobID uint32, env *cleanup.Env, ids []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	var total uint64
	succeeded, failed := 0, 0
	for i, id := range ids {
		s.emit("Progress", jobID, id, uint32(i*100/len(ids)), "limpando "+id)
		freed, result, err := s.clean(ctx, env, id)
		if err != nil {
			log.Printf("vegad: limpeza %s: %v", id, err)
		}
		switch result {
		case cleanup.ResultOK:
			succeeded++
		case cleanup.ResultFailed:
			failed++
		}
		total += freed
		s.emit("CategoryFinished", jobID, id, result, freed)
	}
	result := "ok"
	switch {
	case failed > 0 && succeeded == 0:
		result = "failed"
	case failed > 0:
		result = "partial"
	}
	s.emit("Progress", jobID, ids[len(ids)-1], uint32(100), "")
	s.emit("Finished", jobID, result, total)
}

func (s *CleanupService) emit(signal string, args ...interface{}) {
	if s.onEmit != nil {
		s.onEmit(signal, args...)
	}
	if s.conn == nil {
		return
	}
	if err := s.conn.Emit(ObjectPath, BusName+".Cleanup."+signal, args...); err != nil {
		log.Printf("vegad: emit Cleanup.%s: %v", signal, err)
	}
}
