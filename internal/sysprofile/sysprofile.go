// Package sysprofile applies and undoes the fixed system profiles of
// org.lyraos.Vega1.Profiles (system-profiles-v1). vegad never accepts free
// keys or values: only the ids below exist. Every change runs as a list of
// steps, each with its undo; when a step fails the steps already done are
// undone in reverse order, so a profile is either fully on or fully off.
// What a profile changed (previous values, packages and group membership it
// added) is kept in a state file, so disabling undoes only that.
package sysprofile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	SAPABAPTrial          = "sap-abap-trial"
	AudioLowLatency       = "audio-lowlatency"
	AudioLowLatencyKernel = "audio-lowlatency-kernel"
)

// IDs lists every known profile in the order ListSystemProfiles returns them.
var IDs = []string{SAPABAPTrial, AudioLowLatency, AudioLowLatencyKernel}

func Known(id string) bool {
	for _, known := range IDs {
		if known == id {
			return true
		}
	}
	return false
}

// DefaultStateDir keeps one JSON file per enabled profile.
const DefaultStateDir = "/var/lib/vega/profiles"

const (
	sapSysctlFile  = "/etc/sysctl.d/90-lyra-sap-abap-trial.conf"
	audioLimitFile = "/etc/security/limits.d/90-lyra-audio.conf"
	jackPackage    = "pipewire-jack"
	audioGroup     = "audio"
	// audioRTPrio is the rtprio the limits file grants the audio group.
	audioRTPrio = 95
)

// sapSysctl holds the minimum values the SAP ABAP Platform Trial needs. A
// value already at or above the minimum is never lowered (fs.file-max is
// usually far higher by default).
var sapSysctl = []struct {
	Key   string
	Value uint64
}{
	{"vm.max_map_count", 2147483647},
	{"fs.file-max", 20000000},
	{"fs.aio-max-nr", 18446744073709551615},
}

const audioLimits = `# Lyra OS: perfil "audio-lowlatency" do Vega (vegad).
# Tempo real e memória travada para o grupo audio, como no Ubuntu Studio.
# Desligue o perfil no Vega em vez de editar este arquivo.
@audio - rtprio 95
@audio - memlock unlimited
`

// kernelParams go to the kernel command line for audio-lowlatency-kernel.
var kernelParams = []string{"preempt=full", "threadirqs"}

// Bootloader reads and writes the persistent kernel command line (the part
// vegad manages, without root= or the image).
type Bootloader interface {
	Cmdline() (string, error)
	SetCmdline(cmdline string) error
}

// Caller identifies who asked for the change; Username is empty for root.
type Caller struct {
	Uid      uint32
	Username string
}

// Env is the host access of a profile change; tests replace every field.
type Env struct {
	// Root prefixes every file path ("" on the host).
	Root     string
	StateDir string
	// Run executes a command and returns its combined output.
	Run        func(name string, args ...string) (string, error)
	Bootloader Bootloader
	Logf       func(format string, args ...interface{})
}

func (e *Env) path(name string) string { return filepath.Join(e.Root, name) }

func (e *Env) logf(format string, args ...interface{}) {
	if e.Logf != nil {
		e.Logf(format, args...)
	}
}

// State is what an enabled profile changed.
type State struct {
	EnabledBy uint32    `json:"enabledBy"`
	EnabledAt time.Time `json:"enabledAt"`
	// Sysctl maps each raised key to its previous value (sap-abap-trial).
	Sysctl map[string]string `json:"sysctl,omitempty"`
	// audio-lowlatency
	User          string `json:"user,omitempty"`
	AddedToGroup  bool   `json:"addedToGroup,omitempty"`
	InstalledJack bool   `json:"installedJack,omitempty"`
	// audio-lowlatency-kernel
	PreviousCmdline string   `json:"previousCmdline,omitempty"`
	WrittenCmdline  string   `json:"writtenCmdline,omitempty"`
	AddedParams     []string `json:"addedParams,omitempty"`
}

func (e *Env) statePath(id string) string {
	return filepath.Join(e.Root, e.StateDir, id+".json")
}

// Enabled reports whether the profile is applied: its state file exists.
func Enabled(env *Env, id string) bool {
	_, err := os.Stat(env.statePath(id))
	return err == nil
}

func loadState(env *Env, id string) (*State, error) {
	data, err := os.ReadFile(env.statePath(id))
	if err != nil {
		return nil, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("estado do perfil %s ilegível: %w", id, err)
	}
	return &state, nil
}

func saveState(env *Env, id string, state *State) error {
	path := env.statePath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, data, 0o600)
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".vega-tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// step is one reversible change.
type step struct {
	message string
	do      func() error
	undo    func() error
}

// Progress reports a step: percent of the job and a short message.
type Progress func(percent uint32, message string)

// Apply enables or disables one profile. Asking for the current state is a
// no-op. On error the steps already done were undone.
func Apply(env *Env, id string, enable bool, caller Caller, progress Progress) error {
	if !Known(id) {
		return fmt.Errorf("perfil desconhecido: %s", id)
	}
	if progress == nil {
		progress = func(uint32, string) {}
	}
	if Enabled(env, id) == enable {
		return nil
	}
	var (
		state *State
		steps []step
		err   error
	)
	if enable {
		state = &State{EnabledBy: caller.Uid, EnabledAt: time.Now().UTC()}
		steps, err = enableSteps(env, id, state, caller)
	} else {
		state, err = loadState(env, id)
		if err == nil {
			steps, err = disableSteps(env, id, state)
		}
	}
	if err != nil {
		return err
	}
	var done []step
	for i, s := range steps {
		progress(uint32(i*100/len(steps)), s.message)
		if err := s.do(); err != nil {
			return rollback(env, id, done, fmt.Errorf("%s: %w", s.message, err))
		}
		done = append(done, s)
	}
	if enable {
		err = saveState(env, id, state)
	} else {
		err = removeFile(env.statePath(id))
	}
	if err != nil {
		return rollback(env, id, done, fmt.Errorf("gravar o estado do perfil: %w", err))
	}
	progress(100, "")
	return nil
}

func rollback(env *Env, id string, done []step, cause error) error {
	errs := []error{cause}
	for i := len(done) - 1; i >= 0; i-- {
		if done[i].undo == nil {
			continue
		}
		if err := done[i].undo(); err != nil {
			env.logf("vegad: perfil %s: desfazer \"%s\" falhou: %v", id, done[i].message, err)
			errs = append(errs, fmt.Errorf("desfazer %s: %w", done[i].message, err))
		}
	}
	return errors.Join(errs...)
}

func enableSteps(env *Env, id string, state *State, caller Caller) ([]step, error) {
	switch id {
	case SAPABAPTrial:
		return sapEnable(env, state)
	case AudioLowLatency:
		return audioEnable(env, state, caller)
	default:
		return kernelEnable(env, state)
	}
}

func disableSteps(env *Env, id string, state *State) ([]step, error) {
	switch id {
	case SAPABAPTrial:
		return sapDisable(env, state), nil
	case AudioLowLatency:
		return audioDisable(env, state), nil
	default:
		return kernelDisable(env, state)
	}
}

// --- sap-abap-trial ------------------------------------------------------

func (e *Env) sysctlPath(key string) string {
	return e.path("/proc/sys/" + strings.ReplaceAll(key, ".", "/"))
}

func sapEnable(env *Env, state *State) ([]step, error) {
	state.Sysctl = map[string]string{}
	var content strings.Builder
	content.WriteString("# Lyra OS: perfil \"sap-abap-trial\" do Vega (vegad).\n")
	content.WriteString("# Valores mínimos do SAP ABAP Platform Trial; os que já eram maiores ficam.\n")
	content.WriteString("# Desligue o perfil no Vega em vez de editar este arquivo.\n")
	for _, want := range sapSysctl {
		raw, err := os.ReadFile(env.sysctlPath(want.Key))
		if err != nil {
			return nil, fmt.Errorf("ler %s: %w", want.Key, err)
		}
		current := strings.TrimSpace(string(raw))
		value, err := strconv.ParseUint(current, 10, 64)
		if err == nil && value >= want.Value {
			continue
		}
		state.Sysctl[want.Key] = current
		fmt.Fprintf(&content, "%s = %d\n", want.Key, want.Value)
	}
	file := env.path(sapSysctlFile)
	return []step{{
		message: "gravando " + sapSysctlFile,
		do:      func() error { return writeFile(file, []byte(content.String()), 0o644) },
		undo:    func() error { return removeFile(file) },
	}, {
		message: "aplicando os valores do kernel",
		do: func() error {
			_, err := env.Run("sysctl", "-p", file)
			return err
		},
		undo: func() error { return restoreSysctl(env, state.Sysctl) },
	}}, nil
}

func sapDisable(env *Env, state *State) []step {
	file := env.path(sapSysctlFile)
	previous, _ := os.ReadFile(file)
	return []step{{
		message: "removendo " + sapSysctlFile,
		do:      func() error { return removeFile(file) },
		undo:    func() error { return writeFile(file, previous, 0o644) },
	}, {
		message: "restaurando os valores do kernel",
		do:      func() error { return restoreSysctl(env, state.Sysctl) },
		undo: func() error {
			_, err := env.Run("sysctl", "-p", file)
			return err
		},
	}}
}

func restoreSysctl(env *Env, values map[string]string) error {
	var errs []error
	for _, want := range sapSysctl {
		previous, ok := values[want.Key]
		if !ok {
			continue
		}
		if _, err := strconv.ParseUint(previous, 10, 64); err != nil {
			errs = append(errs, fmt.Errorf("valor anterior inválido de %s: %q", want.Key, previous))
			continue
		}
		if _, err := env.Run("sysctl", "-w", want.Key+"="+previous); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// --- audio-lowlatency ----------------------------------------------------

func validUsername(name string) bool {
	if name == "" || len(name) > 64 || strings.HasPrefix(name, "-") {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-$", r)) {
			return false
		}
	}
	return true
}

func inGroup(env *Env, user, group string) (bool, error) {
	out, err := env.Run("id", "-nG", "--", user)
	if err != nil {
		return false, fmt.Errorf("consultar os grupos de %s: %w", user, err)
	}
	for _, name := range strings.Fields(out) {
		if name == group {
			return true, nil
		}
	}
	return false, nil
}

func packageInstalled(env *Env, name string) bool {
	_, err := env.Run("rpm", "-q", "--quiet", name)
	return err == nil
}

func installJack(env *Env) error {
	_, err := env.Run("zypper", "--non-interactive", "install", "--no-recommends", jackPackage)
	return err
}

func removeJack(env *Env) error {
	_, err := env.Run("zypper", "--non-interactive", "remove", "--no-clean-deps", jackPackage)
	return err
}

func audioEnable(env *Env, state *State, caller Caller) ([]step, error) {
	var steps []step
	if !packageInstalled(env, jackPackage) {
		state.InstalledJack = true
		steps = append(steps, step{
			message: "instalando " + jackPackage,
			do:      func() error { return installJack(env) },
			undo:    func() error { return removeJack(env) },
		})
	}
	file := env.path(audioLimitFile)
	steps = append(steps, step{
		message: "gravando " + audioLimitFile,
		do:      func() error { return writeFile(file, []byte(audioLimits), 0o644) },
		undo:    func() error { return removeFile(file) },
	})
	if caller.Username != "" {
		if !validUsername(caller.Username) {
			return nil, fmt.Errorf("nome de usuário inválido: %q", caller.Username)
		}
		member, err := inGroup(env, caller.Username, audioGroup)
		if err != nil {
			return nil, err
		}
		state.User = caller.Username
		if !member {
			state.AddedToGroup = true
			user := caller.Username
			steps = append(steps, step{
				message: "incluindo " + user + " no grupo " + audioGroup,
				do:      func() error { _, err := env.Run("gpasswd", "-a", user, audioGroup); return err },
				undo:    func() error { _, err := env.Run("gpasswd", "-d", user, audioGroup); return err },
			})
		}
	}
	return steps, nil
}

func audioDisable(env *Env, state *State) []step {
	file := env.path(audioLimitFile)
	previous, _ := os.ReadFile(file)
	steps := []step{{
		message: "removendo " + audioLimitFile,
		do:      func() error { return removeFile(file) },
		undo:    func() error { return writeFile(file, previous, 0o644) },
	}}
	if state.AddedToGroup && validUsername(state.User) {
		user := state.User
		steps = append(steps, step{
			message: "tirando " + user + " do grupo " + audioGroup,
			do: func() error {
				member, err := inGroup(env, user, audioGroup)
				if err != nil || !member {
					return err
				}
				_, err = env.Run("gpasswd", "-d", user, audioGroup)
				return err
			},
			undo: func() error { _, err := env.Run("gpasswd", "-a", user, audioGroup); return err },
		})
	}
	// pipewire-jack stays when anything installed later needs it (a JACK
	// app from the repositories): removing it would take that app along.
	if state.InstalledJack && packageInstalled(env, jackPackage) {
		if _, err := env.Run("rpm", "-e", "--test", jackPackage); err != nil {
			env.logf("vegad: perfil %s: %s fica instalado, outros pacotes dependem dele", AudioLowLatency, jackPackage)
		} else {
			steps = append(steps, step{
				message: "removendo " + jackPackage,
				do:      func() error { return removeJack(env) },
				undo:    func() error { return installJack(env) },
			})
		}
	}
	return steps
}

// --- audio-lowlatency-kernel ---------------------------------------------

func paramName(token string) string {
	name, _, _ := strings.Cut(token, "=")
	return name
}

// withKernelParams adds the profile parameters, replacing any other preempt=.
func withKernelParams(cmdline string) (string, []string) {
	tokens := strings.Fields(cmdline)
	var added []string
	for _, param := range kernelParams {
		found := false
		kept := tokens[:0]
		for _, token := range tokens {
			switch {
			case token == param:
				found = true
				kept = append(kept, token)
			case paramName(token) == paramName(param) && strings.Contains(param, "="):
				// another preempt= value: replaced by ours
			default:
				kept = append(kept, token)
			}
		}
		tokens = kept
		if !found {
			tokens = append(tokens, param)
			added = append(added, param)
		}
	}
	return strings.Join(tokens, " "), added
}

func withoutParams(cmdline string, params []string) string {
	var kept []string
	for _, token := range strings.Fields(cmdline) {
		drop := false
		for _, param := range params {
			if token == param {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, token)
		}
	}
	return strings.Join(kept, " ")
}

func kernelEnable(env *Env, state *State) ([]step, error) {
	if env.Bootloader == nil {
		return nil, errors.New("bootloader não suportado: só o GRUB é ajustado por este perfil")
	}
	previous, err := env.Bootloader.Cmdline()
	if err != nil {
		return nil, fmt.Errorf("ler a linha de comando do kernel: %w", err)
	}
	written, added := withKernelParams(previous)
	state.PreviousCmdline = previous
	state.WrittenCmdline = written
	state.AddedParams = added
	if written == previous {
		return nil, nil
	}
	return []step{{
		message: "ajustando a linha de comando do kernel",
		do:      func() error { return env.Bootloader.SetCmdline(written) },
		undo:    func() error { return env.Bootloader.SetCmdline(previous) },
	}}, nil
}

func kernelDisable(env *Env, state *State) ([]step, error) {
	if env.Bootloader == nil {
		return nil, errors.New("bootloader não suportado: só o GRUB é ajustado por este perfil")
	}
	current, err := env.Bootloader.Cmdline()
	if err != nil {
		return nil, fmt.Errorf("ler a linha de comando do kernel: %w", err)
	}
	// Untouched since enabling: restore the exact previous line. Changed by
	// someone else meanwhile: take out only what this profile added.
	target := state.PreviousCmdline
	if current != state.WrittenCmdline {
		target = withoutParams(current, state.AddedParams)
	}
	if target == current {
		return nil, nil
	}
	return []step{{
		message: "restaurando a linha de comando do kernel",
		do:      func() error { return env.Bootloader.SetCmdline(target) },
		undo:    func() error { return env.Bootloader.SetCmdline(current) },
	}}, nil
}

// --- restart pending -----------------------------------------------------

// RestartPending reports whether the profile state differs from what the
// running system (kernel profile) or the caller's session (audio profile)
// sees. callerPid 0 means unknown: no session to compare with.
func RestartPending(env *Env, id string, enabled bool, callerPid uint32) bool {
	switch id {
	case AudioLowLatencyKernel:
		data, err := os.ReadFile(env.path("/proc/cmdline"))
		if err != nil {
			return false
		}
		running := true
		tokens := strings.Fields(string(data))
		for _, param := range kernelParams {
			if !contains(tokens, param) {
				running = false
			}
		}
		return enabled != running
	case AudioLowLatency:
		if callerPid == 0 {
			return false
		}
		rtprio, ok := realtimeLimit(env, callerPid)
		if !ok {
			return false
		}
		return enabled != (rtprio >= audioRTPrio)
	default:
		return false
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// realtimeLimit reads the soft "Max realtime priority" of a process.
func realtimeLimit(env *Env, pid uint32) (uint64, bool) {
	data, err := os.ReadFile(env.path(fmt.Sprintf("/proc/%d/limits", pid)))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "Max realtime priority") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "Max realtime priority"))
		if len(fields) == 0 {
			return 0, false
		}
		if fields[0] == "unlimited" {
			return ^uint64(0), true
		}
		value, err := strconv.ParseUint(fields[0], 10, 64)
		return value, err == nil
	}
	return 0, false
}
