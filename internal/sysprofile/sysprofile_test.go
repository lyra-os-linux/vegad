package sysprofile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeHost struct {
	t        *testing.T
	root     string
	calls    []string
	packages map[string]bool
	groups   map[string]bool
	// rpmEraseBlocked makes "rpm -e --test" fail (something depends on it).
	rpmEraseBlocked bool
	// fail makes the command with this prefix fail.
	fail    string
	cmdline string
	setErr  error
}

func newHost(t *testing.T) (*fakeHost, *Env) {
	t.Helper()
	h := &fakeHost{t: t, root: t.TempDir(), packages: map[string]bool{}, groups: map[string]bool{}, cmdline: "quiet splash"}
	h.write("/proc/sys/vm/max_map_count", "65530\n")
	h.write("/proc/sys/fs/file-max", "9223372036854775807\n")
	h.write("/proc/sys/fs/aio-max-nr", "65536\n")
	env := &Env{Root: h.root, StateDir: DefaultStateDir, Run: h.run, Bootloader: h}
	return h, env
}

func (h *fakeHost) write(name, content string) {
	h.t.Helper()
	path := filepath.Join(h.root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *fakeHost) exists(name string) bool {
	_, err := os.Stat(filepath.Join(h.root, name))
	return err == nil
}

func (h *fakeHost) run(name string, args ...string) (string, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	h.calls = append(h.calls, call)
	if h.fail != "" && strings.HasPrefix(call, h.fail) {
		return "falhou", errors.New("exit status 1")
	}
	switch {
	case name == "rpm" && args[0] == "-q":
		if !h.packages[args[len(args)-1]] {
			return "", errors.New("not installed")
		}
	case name == "rpm" && args[0] == "-e":
		if h.rpmEraseBlocked {
			return "", errors.New("needed by ardour")
		}
	case name == "zypper" && args[1] == "install":
		h.packages[args[len(args)-1]] = true
	case name == "zypper" && args[1] == "remove":
		delete(h.packages, args[len(args)-1])
	case name == "id":
		if h.groups[args[len(args)-1]] {
			return "users audio", nil
		}
		return "users", nil
	case name == "gpasswd" && args[0] == "-a":
		h.groups[args[1]] = true
	case name == "gpasswd" && args[0] == "-d":
		delete(h.groups, args[1])
	}
	return "", nil
}

func (h *fakeHost) Cmdline() (string, error) { return h.cmdline, nil }
func (h *fakeHost) SetCmdline(value string) error {
	if h.setErr != nil {
		return h.setErr
	}
	h.cmdline = value
	return nil
}

func (h *fakeHost) has(prefix string) bool {
	for _, call := range h.calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

func TestUnknownProfileIsRejected(t *testing.T) {
	_, env := newHost(t)
	if err := Apply(env, "kernel.anything", true, Caller{}, nil); err == nil {
		t.Fatal("perfil desconhecido aceito")
	}
	if Known("") || Known("../sap-abap-trial") {
		t.Fatal("Known aceita id fora da lista")
	}
}

func TestSAPRaisesOnlyLowValuesAndRestoresThem(t *testing.T) {
	h, env := newHost(t)
	if err := Apply(env, SAPABAPTrial, true, Caller{Uid: 1000}, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(h.root, sapSysctlFile))
	if err != nil {
		t.Fatal(err)
	}
	conf := string(data)
	if !strings.Contains(conf, "vm.max_map_count = 2147483647") || !strings.Contains(conf, "fs.aio-max-nr = 18446744073709551615") {
		t.Fatalf("arquivo sem os valores do SAP:\n%s", conf)
	}
	if strings.Contains(conf, "fs.file-max") {
		t.Fatalf("fs.file-max já era maior e não pode baixar:\n%s", conf)
	}
	if !h.has("sysctl -p "+filepath.Join(h.root, sapSysctlFile)) || !Enabled(env, SAPABAPTrial) {
		t.Fatalf("perfil não aplicado: %v", h.calls)
	}

	// Idempotent: enabling again changes nothing.
	calls := len(h.calls)
	if err := Apply(env, SAPABAPTrial, true, Caller{}, nil); err != nil || len(h.calls) != calls {
		t.Fatalf("religar não é no-op: %v %v", err, h.calls[calls:])
	}

	if err := Apply(env, SAPABAPTrial, false, Caller{}, nil); err != nil {
		t.Fatal(err)
	}
	if h.exists(sapSysctlFile) || Enabled(env, SAPABAPTrial) {
		t.Fatal("desligar deixou o arquivo ou o estado")
	}
	if !h.has("sysctl -w vm.max_map_count=65530") || !h.has("sysctl -w fs.aio-max-nr=65536") || h.has("sysctl -w fs.file-max") {
		t.Fatalf("valores anteriores não restaurados: %v", h.calls)
	}
	if err := Apply(env, SAPABAPTrial, false, Caller{}, nil); err != nil {
		t.Fatalf("desligar de novo não é no-op: %v", err)
	}
}

func TestSAPFailureUndoesTheFile(t *testing.T) {
	h, env := newHost(t)
	h.fail = "sysctl -p"
	if err := Apply(env, SAPABAPTrial, true, Caller{}, nil); err == nil {
		t.Fatal("falha do sysctl não reportada")
	}
	if h.exists(sapSysctlFile) || Enabled(env, SAPABAPTrial) {
		t.Fatal("falha deixou o perfil meio ligado")
	}
}

func TestAudioInstallsJackWritesLimitsAndAddsGroup(t *testing.T) {
	h, env := newHost(t)
	caller := Caller{Uid: 1000, Username: "maria"}
	if err := Apply(env, AudioLowLatency, true, caller, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(h.root, audioLimitFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "@audio - rtprio 95") || !strings.Contains(string(data), "@audio - memlock unlimited") {
		t.Fatalf("limites errados:\n%s", data)
	}
	if !h.packages[jackPackage] || !h.groups["maria"] {
		t.Fatalf("pipewire-jack ou grupo ausente: %v", h.calls)
	}

	if err := Apply(env, AudioLowLatency, false, Caller{}, nil); err != nil {
		t.Fatal(err)
	}
	if h.exists(audioLimitFile) || h.packages[jackPackage] || h.groups["maria"] || Enabled(env, AudioLowLatency) {
		t.Fatalf("desligar deixou restos: %v", h.calls)
	}
}

func TestAudioKeepsWhatWasThereBefore(t *testing.T) {
	h, env := newHost(t)
	h.packages[jackPackage] = true
	h.groups["maria"] = true
	if err := Apply(env, AudioLowLatency, true, Caller{Uid: 1000, Username: "maria"}, nil); err != nil {
		t.Fatal(err)
	}
	if h.has("zypper") || h.has("gpasswd") {
		t.Fatalf("mexeu no que já existia: %v", h.calls)
	}
	if err := Apply(env, AudioLowLatency, false, Caller{}, nil); err != nil {
		t.Fatal(err)
	}
	if !h.packages[jackPackage] || !h.groups["maria"] {
		t.Fatal("desligar tirou o que o perfil não pôs")
	}
}

func TestAudioKeepsJackWhenSomethingNeedsIt(t *testing.T) {
	h, env := newHost(t)
	if err := Apply(env, AudioLowLatency, true, Caller{Uid: 1000, Username: "maria"}, nil); err != nil {
		t.Fatal(err)
	}
	h.rpmEraseBlocked = true
	if err := Apply(env, AudioLowLatency, false, Caller{}, nil); err != nil {
		t.Fatal(err)
	}
	if !h.packages[jackPackage] || h.has("zypper --non-interactive remove") {
		t.Fatal("pipewire-jack removido apesar de dependentes")
	}
}

func TestAudioFailureRollsBackEverything(t *testing.T) {
	h, env := newHost(t)
	h.fail = "gpasswd -a"
	if err := Apply(env, AudioLowLatency, true, Caller{Uid: 1000, Username: "maria"}, nil); err == nil {
		t.Fatal("falha do gpasswd não reportada")
	}
	if h.exists(audioLimitFile) || h.packages[jackPackage] || Enabled(env, AudioLowLatency) {
		t.Fatalf("reversão incompleta: %v", h.calls)
	}
}

func TestAudioRejectsOddUsernames(t *testing.T) {
	_, env := newHost(t)
	if err := Apply(env, AudioLowLatency, true, Caller{Uid: 1000, Username: "-o"}, nil); err == nil {
		t.Fatal("nome de usuário inválido aceito")
	}
}

func TestKernelAddsParamsAndRestoresExactLine(t *testing.T) {
	h, env := newHost(t)
	h.cmdline = "quiet  preempt=voluntary splash"
	if err := Apply(env, AudioLowLatencyKernel, true, Caller{}, nil); err != nil {
		t.Fatal(err)
	}
	if h.cmdline != "quiet splash preempt=full threadirqs" {
		t.Fatalf("cmdline = %q", h.cmdline)
	}
	if err := Apply(env, AudioLowLatencyKernel, false, Caller{}, nil); err != nil {
		t.Fatal(err)
	}
	if h.cmdline != "quiet  preempt=voluntary splash" {
		t.Fatalf("linha original não restaurada: %q", h.cmdline)
	}
}

func TestKernelDisableKeepsChangesMadeMeanwhile(t *testing.T) {
	h, env := newHost(t)
	if err := Apply(env, AudioLowLatencyKernel, true, Caller{}, nil); err != nil {
		t.Fatal(err)
	}
	h.cmdline += " mitigations=auto"
	if err := Apply(env, AudioLowLatencyKernel, false, Caller{}, nil); err != nil {
		t.Fatal(err)
	}
	if h.cmdline != "quiet splash mitigations=auto" {
		t.Fatalf("cmdline = %q", h.cmdline)
	}
}

func TestKernelWithoutGrubIsRefused(t *testing.T) {
	_, env := newHost(t)
	env.Bootloader = nil
	if err := Apply(env, AudioLowLatencyKernel, true, Caller{}, nil); err == nil || Enabled(env, AudioLowLatencyKernel) {
		t.Fatal("perfil de kernel aceito sem GRUB")
	}
}

func TestKernelSetFailureLeavesProfileOff(t *testing.T) {
	h, env := newHost(t)
	h.setErr = errors.New("grub2-mkconfig falhou")
	if err := Apply(env, AudioLowLatencyKernel, true, Caller{}, nil); err == nil || Enabled(env, AudioLowLatencyKernel) {
		t.Fatal("falha do GRUB deixou o perfil ligado")
	}
}

func TestRestartPending(t *testing.T) {
	h, env := newHost(t)
	h.write("/proc/cmdline", "BOOT_IMAGE=/boot/vmlinuz quiet\n")
	if RestartPending(env, AudioLowLatencyKernel, false, 0) || !RestartPending(env, AudioLowLatencyKernel, true, 0) {
		t.Fatal("kernel: pendência errada antes de reiniciar")
	}
	h.write("/proc/cmdline", "BOOT_IMAGE=/boot/vmlinuz quiet preempt=full threadirqs\n")
	if RestartPending(env, AudioLowLatencyKernel, true, 0) || !RestartPending(env, AudioLowLatencyKernel, false, 0) {
		t.Fatal("kernel: pendência errada depois de reiniciar")
	}

	limits := "Limit                     Soft Limit           Hard Limit           Units     \nMax realtime priority     %s                    %s                    \n"
	h.write("/proc/4242/limits", strings.ReplaceAll(limits, "%s", "0"))
	if !RestartPending(env, AudioLowLatency, true, 4242) || RestartPending(env, AudioLowLatency, false, 4242) {
		t.Fatal("áudio: sessão sem rtprio deveria pedir novo login")
	}
	h.write("/proc/4242/limits", strings.ReplaceAll(limits, "%s", "95"))
	if RestartPending(env, AudioLowLatency, true, 4242) {
		t.Fatal("áudio: sessão com rtprio 95 não precisa de novo login")
	}
	if RestartPending(env, AudioLowLatency, true, 0) || RestartPending(env, SAPABAPTrial, true, 4242) {
		t.Fatal("sem sessão conhecida ou no SAP não há pendência")
	}
}
