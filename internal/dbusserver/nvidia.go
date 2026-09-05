package dbusserver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	nvidiaKMPMeta               = "nvidia-open-driver-G06-signed-kmp-meta"
	nvidiaUserspaceMeta         = "nvidia-userspace-meta-G06"
	nvidiaSleepQuarantinePath   = "/etc/systemd/sleep.conf.d/90-lyra-nvidia-quarantine.conf"
	nvidiaSleepQuarantineMarker = "# Managed by Vega: NVIDIA suspend qualification"
)

// NvidiaStatus is deliberately compact because it is also the stable D-Bus
// wire contract consumed by vega-gtk. State is one of unavailable,
// installed, reboot-required, active or quarantined. Installation is retired.
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
	cmd := systemCommand(name, args...)
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

type nvidiaManager struct {
	run                 nvidiaRunner
	sleepQuarantinePath string
}

func newNvidiaManager() nvidiaManager {
	return nvidiaManager{
		run:                 systemNvidiaRunner{},
		sleepQuarantinePath: nvidiaSleepQuarantinePath,
	}
}

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

func upstreamVersion(evr string) string {
	version := strings.TrimSpace(evr)
	if index := strings.IndexAny(version, "-_"); index >= 0 {
		version = version[:index]
	}
	return version
}

func (m nvidiaManager) installedStackVersions() ([]string, error) {
	out, err := m.run.Output("rpm", "-qa", "--qf", "%{NAME}|%{VERSION}\n")
	if err != nil {
		return nil, fmt.Errorf("não foi possível auditar os pacotes NVIDIA instalados: %w", err)
	}
	versions := make(map[string]struct{})
	for _, line := range strings.Split(out, "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), "|", 2)
		if len(fields) != 2 || !strings.Contains(fields[0], "G06") {
			continue
		}
		if strings.HasPrefix(fields[0], "nvidia-") || strings.HasPrefix(fields[0], "libnvidia-") {
			versions[upstreamVersion(fields[1])] = struct{}{}
		}
	}
	result := make([]string, 0, len(versions))
	for version := range versions {
		result = append(result, version)
	}
	sort.Strings(result)
	return result, nil
}

func (m nvidiaManager) hybridGraphics() bool {
	var hasNvidia, hasIntegrated bool
	for _, class := range []string{"::0300", "::0302"} {
		out, _ := m.run.Output("lspci", "-Dnd", class)
		for _, line := range strings.Split(strings.ToLower(out), "\n") {
			switch {
			case strings.Contains(line, " 10de:"):
				hasNvidia = true
			case strings.Contains(line, " 8086:"), strings.Contains(line, " 1002:"):
				hasIntegrated = true
			}
		}
	}
	return hasNvidia && hasIntegrated
}

func (m nvidiaManager) suspendQualified(version string) (bool, string) {
	// 580.159.03 is quarantined on hybrid systems after reproducible failures
	// in the driver's VRAM mapping path during S3/s2idle entry. Keep this
	// narrow: desktops and later versions are not penalized by this incident.
	if m.hybridGraphics() && upstreamVersion(version) == "580.159.03" {
		return false, "NVIDIA 580.159.03 em notebook híbrido não está qualificada para suspensão"
	}
	return true, ""
}

func (m nvidiaManager) quarantinePath() string {
	if m.sleepQuarantinePath != "" {
		return m.sleepQuarantinePath
	}
	return nvidiaSleepQuarantinePath
}

func (m nvidiaManager) reconcileSuspendPolicy(version string) (bool, error) {
	qualified, reason := m.suspendQualified(version)
	path := m.quarantinePath()
	if !qualified {
		contents := nvidiaSleepQuarantineMarker + "\n# " + reason + "\n[Sleep]\nAllowSuspend=no\nAllowHibernation=no\nAllowSuspendThenHibernate=no\nAllowHybridSleep=no\n"
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return true, fmt.Errorf("não foi possível criar o diretório da quarentena NVIDIA: %w", err)
		}
		temporary := path + ".tmp"
		if err := os.WriteFile(temporary, []byte(contents), 0o644); err != nil {
			return true, fmt.Errorf("não foi possível gravar a quarentena NVIDIA: %w", err)
		}
		if err := os.Rename(temporary, path); err != nil {
			_ = os.Remove(temporary)
			return true, fmt.Errorf("não foi possível ativar a quarentena NVIDIA: %w", err)
		}
		if err := m.run.Run("systemctl", "daemon-reload"); err != nil {
			return true, fmt.Errorf("a quarentena foi gravada, mas o systemd não a recarregou: %w", err)
		}
		return true, nil
	}
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("não foi possível verificar a quarentena NVIDIA: %w", err)
	}
	if !strings.HasPrefix(string(contents), nvidiaSleepQuarantineMarker) {
		return false, fmt.Errorf("%s existe, mas não é gerenciado pelo Vega; remoção automática bloqueada", path)
	}
	if err := os.Remove(path); err != nil {
		return false, fmt.Errorf("não foi possível remover a quarentena NVIDIA obsoleta: %w", err)
	}
	if err := m.run.Run("systemctl", "daemon-reload"); err != nil {
		return false, fmt.Errorf("a quarentena foi removida, mas o systemd não foi recarregado: %w", err)
	}
	return false, nil
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
	status.Detail = "A instalação e a troca de drivers foram removidas do Vega."
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
	stackVersions, stackErr := m.installedStackVersions()
	if stackErr != nil {
		status.State = "unavailable"
		status.Detail = stackErr.Error()
		return status, nil
	}
	if len(stackVersions) > 1 {
		status.State = "unavailable"
		status.Detail = fmt.Sprintf("Pacotes NVIDIA G06 desalinhados nas versões %s; restaure o snapshot antes de reiniciar.", strings.Join(stackVersions, ", "))
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
		if qualified, reason := m.suspendQualified(kmpVersion); !qualified {
			status.State = "quarantined"
			status.Detail = "Driver NVIDIA ativo, mas suspensão e hibernação estão em quarentena: " + reason + "."
		}
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

func (m nvidiaManager) check() error {
	status, err := m.status()
	if err != nil {
		return err
	}
	if status.State != "active" && status.State != "quarantined" {
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
	kmpVersion := m.packageVersion(nvidiaKMPMeta)
	quarantined, err := m.reconcileSuspendPolicy(kmpVersion)
	if err != nil {
		return err
	}
	if quarantined {
		return errors.New("driver e conectores validados; suspensão e hibernação foram bloqueadas por regressão conhecida da NVIDIA 580.159.03 em notebook híbrido")
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

// InstallNvidia is a compatibility endpoint only. It never authorizes or
// starts a package transaction, including for explicitly confirmed requests.
func (s *SoftwareService) InstallNvidia(_ dbus.Sender, _ bool) (uint32, *dbus.Error) {
	return 0, driverManagementRemoved()
}

func (s *SoftwareService) CheckNvidia() (bool, string, *dbus.Error) {
	s.activity.Touch()
	if err := newNvidiaManager().check(); err != nil {
		return false, err.Error(), nil
	}
	return true, "Driver NVIDIA ativo; nvidia-smi e conectores DRM validados.", nil
}

// ReconcileNvidiaSuspendPolicy applies the managed power guard at daemon
// startup as well as during legacy driver checks. This also removes a Vega-owned
// quarantine after a qualified driver replaces the affected version.
func ReconcileNvidiaSuspendPolicy() error {
	manager := newNvidiaManager()
	_, err := manager.reconcileSuspendPolicy(manager.packageVersion(nvidiaKMPMeta))
	return err
}
