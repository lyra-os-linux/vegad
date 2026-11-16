package dbusserver

import (
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/sysprofile"
)

// SystemProfile is the (sbb) row of Profiles.ListSystemProfiles.
type SystemProfile struct {
	Id             string
	Enabled        bool
	RestartPending bool
}

const profilesAction = "org.lyraos.vega.profiles.manage"

// ProfilesService implements org.lyraos.Vega1.Profiles (system-profiles-v1):
// fixed profiles applied and undone one job at a time under the operation
// gate, inside a Snapper snapshot pair.
type ProfilesService struct {
	activity *Activity
	conn     *dbus.Conn
	// newEnv builds the host environment; replaced in tests.
	newEnv func() *sysprofile.Env
	// apply changes one profile; replaced in tests.
	apply func(env *sysprofile.Env, id string, enable bool, caller sysprofile.Caller, progress sysprofile.Progress) error
	// caller resolves who sent the call; replaced in tests.
	caller func(sender dbus.Sender) (sysprofile.Caller, uint32)
	// snapshot wraps the change in a pre/post pair; replaced in tests.
	snapshot func(action string, work func() error) error
	// onEmit observes signals in tests.
	onEmit func(signal string, args ...interface{})

	mu     sync.Mutex
	lastID uint32
}

func newProfilesService(activity *Activity, conn *dbus.Conn, kernel func() string) *ProfilesService {
	s := &ProfilesService{
		activity: activity,
		conn:     conn,
		apply:    sysprofile.Apply,
		snapshot: withSnapshots,
	}
	s.newEnv = func() *sysprofile.Env {
		env := &sysprofile.Env{
			StateDir: sysprofile.DefaultStateDir,
			Run:      runCommandOutput,
			Logf:     log.Printf,
		}
		if grubConfig := kernel(); detectBootloader(grubConfig) == "grub" {
			env.Bootloader = grubCmdlineBootloader{defaultsPath: "/etc/default/grub", configPath: grubConfig}
		}
		return env
	}
	s.caller = s.resolveCaller
	return s
}

// resolveCaller returns the calling account and process; root or an
// unresolvable sender yield an empty caller and pid 0.
func (s *ProfilesService) resolveCaller(sender dbus.Sender) (sysprofile.Caller, uint32) {
	if s.conn == nil || sender == "" {
		return sysprofile.Caller{}, 0
	}
	var pid uint32
	if err := s.conn.BusObject().Call("org.freedesktop.DBus.GetConnectionUnixProcessID", 0, string(sender)).Store(&pid); err != nil {
		pid = 0
	}
	user, err := resolveDesktopUser(s.conn, sender)
	if err != nil || user == nil {
		return sysprofile.Caller{}, pid
	}
	return sysprofile.Caller{Uid: user.Uid, Username: user.Username}, pid
}

// ListSystemProfiles is a public read that runs in the broker: it compares
// the profile with the caller's own session limits.
func (s *ProfilesService) ListSystemProfiles(sender dbus.Sender) ([]SystemProfile, *dbus.Error) {
	s.activity.Touch()
	env := s.newEnv()
	_, pid := s.caller(sender)
	out := make([]SystemProfile, 0, len(sysprofile.IDs))
	for _, id := range sysprofile.IDs {
		enabled := sysprofile.Enabled(env, id)
		out = append(out, SystemProfile{
			Id:             id,
			Enabled:        enabled,
			RestartPending: sysprofile.RestartPending(env, id, enabled, pid),
		})
	}
	return out, nil
}

func (s *ProfilesService) SetSystemProfile(sender dbus.Sender, id string, enabled bool) (uint32, *dbus.Error) {
	s.activity.Touch()
	if !sysprofile.Known(id) {
		return 0, notSupported("perfil desconhecido: " + id)
	}
	if err := requireFirstUpdateIdle(); err != nil {
		return 0, err
	}
	if err := requirePolkit(sender, profilesAction); err != nil {
		return 0, err
	}
	caller, _ := s.caller(sender)
	return s.start(id, enabled, caller)
}

func (s *ProfilesService) start(id string, enabled bool, caller sysprofile.Caller) (uint32, *dbus.Error) {
	if !sysprofile.Known(id) {
		return 0, notSupported("perfil desconhecido: " + id)
	}
	release, busy := operations.beginProfile()
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
	go func() {
		defer done()
		defer release()
		s.run(jobID, env, id, enabled, caller)
	}()
	return jobID, nil
}

func (s *ProfilesService) run(jobID uint32, env *sysprofile.Env, id string, enabled bool, caller sysprofile.Caller) {
	verb := "desligar"
	if enabled {
		verb = "ligar"
	}
	log.Printf("vegad: perfil %s: %s, pedido pelo uid %d", id, verb, caller.Uid)
	progress := func(percent uint32, message string) {
		s.emit("ProfileProgress", jobID, id, percent, message)
	}
	err := s.snapshot(fmt.Sprintf("Perfil do sistema: %s %s", verb, id), func() error {
		return s.apply(env, id, enabled, caller, progress)
	})
	if err != nil {
		log.Printf("vegad: perfil %s: %s falhou e foi desfeito: %v", id, verb, err)
		s.emit("ProfileFinished", jobID, id, "failed", err.Error())
		return
	}
	log.Printf("vegad: perfil %s: %s concluído (uid %d)", id, verb, caller.Uid)
	s.emit("ProfileProgress", jobID, id, uint32(100), "")
	s.emit("ProfileFinished", jobID, id, "ok", "")
}

func (s *ProfilesService) emit(signal string, args ...interface{}) {
	if s.onEmit != nil {
		s.onEmit(signal, args...)
	}
	if s.conn == nil {
		return
	}
	if err := s.conn.Emit(ObjectPath, BusName+".Profiles."+signal, args...); err != nil {
		log.Printf("vegad: emit Profiles.%s: %v", signal, err)
	}
}

// grubCmdlineBootloader changes only GRUB_CMDLINE_LINUX_DEFAULT and rebuilds
// grub.cfg; on a failed rebuild the previous /etc/default/grub comes back.
type grubCmdlineBootloader struct {
	defaultsPath string
	configPath   string
}

func (g grubCmdlineBootloader) Cmdline() (string, error) {
	return grubSettingAt(g.defaultsPath, "GRUB_CMDLINE_LINUX_DEFAULT")
}

func (g grubCmdlineBootloader) SetCmdline(cmdline string) error {
	if err := validateBootValues("", cmdline); err != nil {
		return err
	}
	previous, err := os.ReadFile(g.defaultsPath)
	if err != nil {
		return err
	}
	if err := rewriteKeyValueFile(g.defaultsPath, map[string]string{
		"GRUB_CMDLINE_LINUX_DEFAULT": quoteShell(cmdline),
	}); err != nil {
		return err
	}
	if err := runCommand("grub2-mkconfig", "-o", g.configPath); err != nil {
		if restoreErr := writeConfigAtomically(g.defaultsPath, previous); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("recuperar /etc/default/grub: %w", restoreErr))
		}
		return err
	}
	return nil
}

// grubSettingAt reads one key of a GRUB defaults file; a missing key is "".
func grubSettingAt(path, key string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(key) + `=(.*)$`)
	for _, line := range strings.Split(string(data), "\n") {
		if m := re.FindStringSubmatch(strings.TrimSpace(line)); len(m) == 2 {
			return unquoteShell(strings.TrimSpace(m[1])), nil
		}
	}
	return "", nil
}
