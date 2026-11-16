package dbusserver

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/sysprofile"
)

type profileRecorder struct {
	mu      sync.Mutex
	signals [][]interface{}
	done    chan struct{}
}

func (r *profileRecorder) record(signal string, args ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.signals = append(r.signals, append([]interface{}{signal}, args...))
	if signal == "ProfileFinished" {
		close(r.done)
	}
}

func newTestProfiles(t *testing.T, apply func(id string, enable bool) error) (*ProfilesService, *profileRecorder, *[]string) {
	t.Helper()
	saved := operations
	operations = newTestGate(false)
	t.Cleanup(func() { operations = saved })
	recorder := &profileRecorder{done: make(chan struct{})}
	var snapshots []string
	s := newProfilesService(&Activity{last: time.Now()}, nil, func() string { return "" })
	s.newEnv = func() *sysprofile.Env {
		return &sysprofile.Env{Root: t.TempDir(), StateDir: sysprofile.DefaultStateDir}
	}
	s.apply = func(_ *sysprofile.Env, id string, enable bool, _ sysprofile.Caller, progress sysprofile.Progress) error {
		progress(0, "passo")
		return apply(id, enable)
	}
	s.snapshot = func(action string, work func() error) error {
		snapshots = append(snapshots, action)
		return work()
	}
	s.caller = func(dbus.Sender) (sysprofile.Caller, uint32) { return sysprofile.Caller{}, 0 }
	s.onEmit = recorder.record
	return s, recorder, &snapshots
}

func TestSetSystemProfileRunsInsideSnapshotAndFinishesOK(t *testing.T) {
	s, recorder, snapshots := newTestProfiles(t, func(string, bool) error { return nil })
	job, err := s.start(sysprofile.SAPABAPTrial, true, sysprofile.Caller{Uid: 1000})
	if err != nil {
		t.Fatal(err)
	}
	<-recorder.done
	last := recorder.signals[len(recorder.signals)-1]
	if last[0] != "ProfileFinished" || last[1] != job || last[2] != sysprofile.SAPABAPTrial || last[3] != "ok" {
		t.Fatalf("ProfileFinished = %v", last)
	}
	if len(*snapshots) != 1 || !strings.Contains((*snapshots)[0], sysprofile.SAPABAPTrial) {
		t.Fatalf("snapshot ausente: %v", *snapshots)
	}
}

func TestSetSystemProfileFailureReportsDetail(t *testing.T) {
	s, recorder, _ := newTestProfiles(t, func(string, bool) error { return errors.New("zypper falhou") })
	if _, err := s.start(sysprofile.AudioLowLatency, true, sysprofile.Caller{}); err != nil {
		t.Fatal(err)
	}
	<-recorder.done
	last := recorder.signals[len(recorder.signals)-1]
	if last[3] != "failed" || last[4] != "zypper falhou" {
		t.Fatalf("ProfileFinished = %v", last)
	}
}

func TestSetSystemProfileRejectsUnknownIds(t *testing.T) {
	s, _, _ := newTestProfiles(t, func(string, bool) error { t.Fatal("aplicou perfil desconhecido"); return nil })
	for _, id := range []string{"", "vm.max_map_count", "audio"} {
		if _, err := s.start(id, true, sysprofile.Caller{}); err == nil || err.Name != BusName+".Error.NotSupported" {
			t.Fatalf("%q: erro = %v", id, err)
		}
	}
}

// Negative authorization test: without a D-Bus sender polkit is never even
// asked and nothing runs.
func TestSetSystemProfileWithoutAuthorizationChangesNothing(t *testing.T) {
	s, _, snapshots := newTestProfiles(t, func(string, bool) error { t.Fatal("aplicou sem autorização"); return nil })
	if _, err := s.SetSystemProfile("", sysprofile.SAPABAPTrial, true); err == nil || err.Name != BusName+".Error.AuthorizationFailed" {
		t.Fatalf("erro = %v", err)
	}
	if len(*snapshots) != 0 {
		t.Fatal("snapshot criado sem autorização")
	}
}

func TestProfileJobIsExclusive(t *testing.T) {
	release := make(chan struct{})
	s, recorder, _ := newTestProfiles(t, func(string, bool) error { <-release; return nil })
	if _, err := s.start(sysprofile.SAPABAPTrial, true, sysprofile.Caller{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.start(sysprofile.AudioLowLatency, true, sysprofile.Caller{}); err == nil || err.Name != BusName+".Error.Busy" {
		t.Fatalf("segundo job: %v", err)
	}
	if _, err := operations.beginTransaction(); err == nil {
		t.Fatal("transação aceita durante um perfil")
	}
	close(release)
	<-recorder.done
}

func TestListSystemProfilesReportsEveryKnownProfile(t *testing.T) {
	s, _, _ := newTestProfiles(t, nil)
	root := t.TempDir()
	s.newEnv = func() *sysprofile.Env { return &sysprofile.Env{Root: root, StateDir: sysprofile.DefaultStateDir} }
	state := filepath.Join(root, sysprofile.DefaultStateDir, sysprofile.SAPABAPTrial+".json")
	if err := os.MkdirAll(filepath.Dir(state), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListSystemProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(sysprofile.IDs) {
		t.Fatalf("linhas = %v", rows)
	}
	for i, row := range rows {
		if row.Id != sysprofile.IDs[i] || row.Enabled != (row.Id == sysprofile.SAPABAPTrial) || row.RestartPending {
			t.Fatalf("linha %d = %+v", i, row)
		}
	}
}

func TestGrubCmdlineBootloaderRestoresDefaultsWhenRebuildFails(t *testing.T) {
	dir := t.TempDir()
	defaults := filepath.Join(dir, "grub")
	original := "GRUB_TIMEOUT=8\nGRUB_CMDLINE_LINUX_DEFAULT=\"quiet splash\"\n"
	if err := os.WriteFile(defaults, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	g := grubCmdlineBootloader{defaultsPath: defaults, configPath: filepath.Join(dir, "missing", "grub.cfg")}
	got, err := g.Cmdline()
	if err != nil || got != "quiet splash" {
		t.Fatalf("Cmdline = %q, %v", got, err)
	}
	if err := g.SetCmdline("quiet splash threadirqs"); err == nil {
		t.Skip("grub2-mkconfig gerou um arquivo num caminho inexistente")
	}
	data, _ := os.ReadFile(defaults)
	if string(data) != original {
		t.Fatalf("/etc/default/grub não restaurado:\n%s", data)
	}
}
