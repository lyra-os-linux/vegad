package dbusserver

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	nvidiaRepoAlias     = "repo-nvidia"
	nvidiaRepoURL       = "https://download.nvidia.com/opensuse/leap/16.0/"
	nvidiaKMPMeta       = "nvidia-open-driver-G06-signed-kmp-meta"
	nvidiaUserspaceMeta = "nvidia-userspace-meta-G06"
)

// NvidiaStatus is deliberately compact because it is also the stable D-Bus
// wire contract consumed by vega-gtk. State is one of unavailable,
// available, installed, reboot-required or active.
type NvidiaStatus struct {
	Supported        bool
	Installed        bool
	RebootRequired   bool
	GPU              string
	SecureBoot       string
	State            string
	Detail           string
	RecoverySnapshot uint32
}

type nvidiaRunner interface {
	Output(name string, args ...string) (string, error)
	Run(name string, args ...string) error
}

type systemNvidiaRunner struct{}

func (systemNvidiaRunner) Output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("%s: %w%s", name, err, outputSuffix(text))
	}
	return text, nil
}

func (r systemNvidiaRunner) Run(name string, args ...string) error {
	_, err := r.Output(name, args...)
	return err
}

func outputSuffix(text string) string {
	if text == "" {
		return ""
	}
	return ": " + text
}

type nvidiaManager struct{ run nvidiaRunner }

func newNvidiaManager() nvidiaManager { return nvidiaManager{run: systemNvidiaRunner{}} }

var nvidiaDeviceID = regexp.MustCompile(`(?i)10de:([0-9a-f]{4})`)

// supportedNvidiaDevice is intentionally conservative: NVIDIA allocated
// display device IDs from 0x1e00 onward to Turing and newer generations.
// Older Pascal/Maxwell devices remain on the legacy/proprietary path and are
// never offered this signed open-kernel G06 flow.
func supportedNvidiaDevice(id uint64) bool { return id >= 0x1e00 && id <= 0x2fff }

func (m nvidiaManager) hardware() (string, bool, error) {
	out, err := m.run.Output("lspci", "-Dnd", "10de:")
	if err != nil && strings.TrimSpace(out) == "" {
		return "", false, fmt.Errorf("não foi possível detectar a GPU NVIDIA: %w", err)
	}
	// Keep lspci's output numeric so class and vendor:device parsing remains
	// stable regardless of the local PCI name database.
	for _, line := range strings.Split(out, "\n") {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, " 0300:") && !strings.Contains(lower, " 0302:") {
			continue
		}
		match := nvidiaDeviceID.FindStringSubmatch(lower)
		if len(match) != 2 {
			continue
		}
		device := match[1]
		nameOut, _ := m.run.Output("lspci", "-s", strings.Fields(line)[0])
		name := strings.TrimSpace(nameOut)
		if name == "" {
			name = "NVIDIA " + device
		}
		return name, func() bool { id, e := strconv.ParseUint(device, 16, 16); return e == nil && supportedNvidiaDevice(id) }(), nil
	}
	return "", false, nil
}

func (m nvidiaManager) secureBoot() string {
	out, err := m.run.Output("mokutil", "--sb-state")
	if err != nil {
		return "unknown"
	}
	if strings.Contains(strings.ToLower(out), "enabled") {
		return "enabled"
	}
	if strings.Contains(strings.ToLower(out), "disabled") {
		return "disabled"
	}
	return "unknown"
}

func (m nvidiaManager) packageVersion(name string) string {
	out, err := m.run.Output("rpm", "-q", "--qf", "%{VERSION}-%{RELEASE}", name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (m nvidiaManager) status() (NvidiaStatus, error) {
	gpu, supported, err := m.hardware()
	if err != nil {
		return NvidiaStatus{}, err
	}
	status := NvidiaStatus{Supported: supported, GPU: gpu, SecureBoot: m.secureBoot(), State: "unavailable"}
	if gpu == "" {
		status.Detail = "Nenhuma GPU NVIDIA foi detectada."
		return status, nil
	}
	if !supported {
		status.Detail = "A GPU detectada é anterior à geração Turing e não é compatível com o fluxo G06 assinado."
		return status, nil
	}
	if status.SecureBoot == "unknown" {
		status.Detail = "Não foi possível verificar o estado do Secure Boot; a instalação foi bloqueada para evitar um módulo que não carregue após reiniciar."
		return status, nil
	}
	status.State = "available"
	status.Detail = "GPU G06 compatível; a instalação opcional está disponível."
	kmpVersion := m.packageVersion(nvidiaKMPMeta)
	userspaceVersion := m.packageVersion(nvidiaUserspaceMeta)
	if kmpVersion == "" && userspaceVersion == "" {
		return status, nil
	}
	if kmpVersion == "" || userspaceVersion == "" || kmpVersion != userspaceVersion {
		status.State = "unavailable"
		status.Detail = "Instalação NVIDIA parcial ou desalinhada detectada; faça rollback ou remova os pacotes G06 antes de continuar."
		return status, nil
	}
	status.Installed = true
	status.State = "reboot-required"
	status.RebootRequired = true
	status.Detail = "Driver instalado em lockstep; reinicie para concluir e validar."
	if m.driverActive() {
		status.State = "active"
		status.RebootRequired = false
		status.Detail = "Driver NVIDIA ativo e pacotes G06 alinhados."
	}
	return status, nil
}

func (m nvidiaManager) driverActive() bool {
	devices, err := filepath.Glob("/sys/bus/pci/devices/*/driver")
	if err != nil {
		return false
	}
	bound := false
	for _, driver := range devices {
		target, err := filepath.EvalSymlinks(driver)
		if err == nil && filepath.Base(target) == "nvidia" {
			bound = true
			break
		}
	}
	if !bound {
		return false
	}
	out, err := m.run.Output("nvidia-smi", "--query-gpu=name,driver_version", "--format=csv,noheader")
	return err == nil && strings.TrimSpace(out) != ""
}

func (m nvidiaManager) rejectPartialPackages() error {
	out, _ := m.run.Output("rpm", "-qa", "--qf", "%{NAME}\n")
	var individual []string
	for _, name := range strings.Split(out, "\n") {
		name = strings.TrimSpace(name)
		if strings.HasPrefix(name, "nvidia-") && strings.Contains(name, "G06") && name != nvidiaKMPMeta && name != nvidiaUserspaceMeta {
			individual = append(individual, name)
		}
	}
	if len(individual) > 0 && (m.packageVersion(nvidiaKMPMeta) == "" || m.packageVersion(nvidiaUserspaceMeta) == "") {
		return fmt.Errorf("pacotes G06 individuais já instalados (%s); remova-os ou restaure um snapshot antes de usar a instalação em lockstep", strings.Join(individual, ", "))
	}
	return nil
}

func (m nvidiaManager) snapshot() (uint32, error) {
	out, err := m.run.Output("snapper", "-c", "root", "create", "--type", "single", "--read-only", "--description", "antes do driver NVIDIA G06", "--cleanup-algorithm", "number", "--print-number")
	if err != nil {
		return 0, fmt.Errorf("a instalação foi abortada porque o snapshot de recuperação não pôde ser criado: %w", err)
	}
	id, err := strconv.ParseUint(strings.TrimSpace(out), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("snapper retornou um identificador inválido %q", out)
	}
	return uint32(id), nil
}

func (m nvidiaManager) ensureRepository() error {
	out, err := m.run.Output("zypper", "--non-interactive", "lr", "-u", nvidiaRepoAlias)
	if err == nil {
		if !strings.Contains(out, nvidiaRepoURL) {
			return fmt.Errorf("o repositório %s já existe com outra URL; esperado %s", nvidiaRepoAlias, nvidiaRepoURL)
		}
	} else if err := m.run.Run("zypper", "--non-interactive", "addrepo", "--refresh", "--check", nvidiaRepoURL, nvidiaRepoAlias); err != nil {
		return fmt.Errorf("não foi possível adicionar o repositório NVIDIA oficial: %w", err)
	}
	if err := m.run.Run("zypper", "--non-interactive", "--gpg-auto-import-keys", "refresh", nvidiaRepoAlias); err != nil {
		return fmt.Errorf("não foi possível validar/atualizar o repositório NVIDIA: %w", err)
	}
	return nil
}

func (m nvidiaManager) install(report progressFunc) (uint32, error) {
	status, err := m.status()
	if err != nil {
		return 0, err
	}
	if !status.Supported {
		return 0, errors.New(status.Detail)
	}
	if status.State == "unavailable" {
		return 0, errors.New(status.Detail)
	}
	if status.Installed {
		return 0, errors.New("o driver NVIDIA G06 já está instalado; reinicie e use Verificar driver")
	}
	if err := m.rejectPartialPackages(); err != nil {
		return 0, err
	}
	report(5, "Preflight concluído; criando snapshot de recuperação")
	snapshot, err := m.snapshot()
	if err != nil {
		return 0, err
	}
	report(20, fmt.Sprintf("Snapshot %d criado; configurando repositório NVIDIA", snapshot))
	if err := m.ensureRepository(); err != nil {
		return snapshot, fmt.Errorf("%w. Nenhum driver foi instalado; snapshot de recuperação: %d", err, snapshot)
	}
	report(45, "Instalando módulo assinado, userspace e firmware G06 em lockstep")
	if err := m.run.Run("zypper", "--non-interactive", "install", "--no-recommends", nvidiaKMPMeta, nvidiaUserspaceMeta); err != nil {
		return snapshot, fmt.Errorf("a transação NVIDIA falhou; use o snapshot %d para recuperação: %w", snapshot, err)
	}
	kmpVersion := m.packageVersion(nvidiaKMPMeta)
	userspaceVersion := m.packageVersion(nvidiaUserspaceMeta)
	if kmpVersion == "" || kmpVersion != userspaceVersion {
		return snapshot, fmt.Errorf("a transação deixou KMP e userspace desalinhados; use o snapshot %d para recuperação", snapshot)
	}
	report(80, "Regenerando initramfs")
	if err := m.run.Run("dracut", "--force"); err != nil {
		return snapshot, fmt.Errorf("o initramfs não pôde ser regenerado; use o snapshot %d para recuperação: %w", snapshot, err)
	}
	report(100, fmt.Sprintf("Driver G06 %s instalado. Reinicie e use Verificar driver. Snapshot de recuperação: %d", kmpVersion, snapshot))
	return snapshot, nil
}

func (m nvidiaManager) check() error {
	status, err := m.status()
	if err != nil {
		return err
	}
	if status.State != "active" {
		return errors.New(status.Detail)
	}
	connectors, err := filepath.Glob("/sys/class/drm/card*-*/status")
	if err != nil {
		return fmt.Errorf("não foi possível consultar os conectores DRM: %w", err)
	}
	connectorCount := len(connectors)
	if connectorCount == 0 {
		return errors.New("o driver está ativo, mas nenhum conector DRM foi publicado")
	}
	return nil
}

func (s *SoftwareService) NvidiaStatus() (NvidiaStatus, *dbus.Error) {
	s.activity.Touch()
	status, err := newNvidiaManager().status()
	if err != nil {
		return NvidiaStatus{}, dbus.MakeFailedError(err)
	}
	return status, nil
}

func (s *SoftwareService) InstallNvidia(sender dbus.Sender, confirmed bool) (uint32, *dbus.Error) {
	s.activity.Touch()
	if !confirmed {
		return 0, dbus.MakeFailedError(errors.New("confirmação explícita é obrigatória"))
	}
	if err := requirePolkit(sender, "org.lyraos.vega.software.install-nvidia"); err != nil {
		return 0, err
	}
	return s.startTransaction("Instalação do driver NVIDIA G06", func(report progressFunc, _ packageProgressFunc) error {
		_, err := newNvidiaManager().install(report)
		return err
	}), nil
}

func (s *SoftwareService) CheckNvidia() (bool, string, *dbus.Error) {
	s.activity.Touch()
	if err := newNvidiaManager().check(); err != nil {
		return false, err.Error(), nil
	}
	return true, "Driver NVIDIA ativo; nvidia-smi e conectores DRM validados.", nil
}
