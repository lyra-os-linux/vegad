package dbusserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/distro"
)

const (
	nvidiaKMPMeta               = "nvidia-open-driver-G06-signed-kmp-meta"
	nvidiaIntegrationVersion    = distro.NvidiaVersion
	nvidiaSignedKMP             = distro.NvidiaKMP
	nvidiaRecoveryPath          = "/var/lib/vegad-nvidia/recovery"
	nvidiaSleepQuarantinePath   = "/etc/systemd/sleep.conf.d/90-lyra-nvidia-quarantine.conf"
	nvidiaSleepQuarantineMarker = "# Managed by Vega: NVIDIA suspend qualification"
)

// NvidiaStatus is deliberately compact because it is also the stable D-Bus
// wire contract consumed by vega-gtk. See docs/nvidia.md for state codes.
// Detail contains technical diagnostics; translated explanations belong in the UI.
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	base := systemCommand(name, args...)
	cmd := exec.CommandContext(ctx, base.Path, base.Args[1:]...)
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
	sysRoot             string
	recoveryPath        string
}

func newNvidiaManager() nvidiaManager {
	return nvidiaManager{
		run:                 systemNvidiaRunner{},
		sleepQuarantinePath: nvidiaSleepQuarantinePath,
	}
}

var nvidiaDeviceID = regexp.MustCompile(`(?i)10de:([0-9a-f]{4})`)

func (m nvidiaManager) hardware() (string, bool, error) {
	out, err := m.run.Output("lspci", "-Dnd", "10de:")
	if err != nil {
		return "", false, fmt.Errorf("NVIDIA PCI inventory: %w", err)
	}
	names := []string{}
	supported := true
	for _, line := range strings.Split(out, "\n") {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, " 0300:") && !strings.Contains(lower, " 0302:") {
			continue
		}
		match := nvidiaDeviceID.FindStringSubmatch(lower)
		if len(match) != 2 {
			return "", false, errors.New("Invalid NVIDIA PCI inventory")
		}
		id, err := strconv.ParseUint(match[1], 16, 16)
		supported = supported && err == nil && supportedNvidiaDevice(id)
		name, _ := m.run.Output("lspci", "-s", strings.Fields(line)[0])
		if name == "" {
			name = "NVIDIA " + match[1]
		}
		names = append(names, strings.TrimSpace(name))
	}
	return strings.Join(names, "\n"), supported && len(names) > 0, nil
}

func (m nvidiaManager) secureBoot() string {
	out, err := m.run.Output("mokutil", "--sb-state")
	if err != nil {
		if strings.Contains(out, "EFI variables are not supported") {
			if _, efiErr := os.Stat(m.sysPath("firmware/efi")); errors.Is(efiErr, os.ErrNotExist) {
				return "not-applicable"
			}
		}
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

var nvidiaRequiredPackages = []string{
	nvidiaSignedKMP, "nvidia-open", "nvidia-common-G07", "nvidia-compute-G07",
	"nvidia-compute-utils-G07", "nvidia-gl-G07", "nvidia-video-G07",
	"nvidia-modprobe", "nvidia-persistenced",
}

func (m nvidiaManager) packages() (map[string][]string, error) {
	out, err := m.run.Output("rpm", "-qa", "--qf", "%{NAME}|%{VERSION}\n")
	if err != nil {
		return nil, fmt.Errorf("RPM inventory: %w", err)
	}
	packages := make(map[string][]string)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.SplitN(line, "|", 2)
		if len(fields) == 2 {
			packages[fields[0]] = append(packages[fields[0]], fields[1])
		}
	}
	return packages, nil
}

// Standalone EGL platform libraries have their own versions and are not
// NVIDIA driver leaves. Old generations and alternative KMPs are blocked.
func conflictingNvidiaPackage(name string) bool {
	return ((strings.HasPrefix(name, "nvidia-") || strings.HasPrefix(name, "libnvidia-")) &&
		(strings.Contains(name, "G04") || strings.Contains(name, "G05") || strings.Contains(name, "G06"))) ||
		name == "nvidia-open-driver-G07" || name == "nvidia-driver-G07" ||
		(strings.HasPrefix(name, "nvidia-") && strings.Contains(name, "kmp") && name != nvidiaSignedKMP)
}

func (m nvidiaManager) sysPath(path string) string {
	root := m.sysRoot
	if root == "" {
		root = "/sys"
	}
	return filepath.Join(root, path)
}

func (m nvidiaManager) recoveryFile() string {
	if m.recoveryPath != "" {
		return m.recoveryPath
	}
	return nvidiaRecoveryPath
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
		if existing, err := os.ReadFile(path); err == nil && !strings.HasPrefix(string(existing), nvidiaSleepQuarantineMarker) {
			return true, fmt.Errorf("%s não é gerenciado pelo Vega", path)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return true, err
		}
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
	status := NvidiaStatus{Supported: supported, GPU: gpu, SecureBoot: m.secureBoot(), State: "no-gpu"}
	if data, err := os.ReadFile(m.recoveryFile()); err == nil {
		id, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
		status.RecoverySnapshot = uint32(id)
	}
	if gpu == "" {
		return status, nil
	}
	if !supported {
		status.State = "unsupported-gpu"
		return status, nil
	}
	packages, err := m.packages()
	if err != nil {
		return status, err
	}
	arch, archErr := m.run.Output("uname", "-m")
	if archErr != nil || arch != "x86_64" || !containsVersion(packages["Leap-release"], "16.1") {
		status.Supported = false
		status.State = "unsupported-system"
		return status, nil
	}
	for name := range packages {
		if (strings.HasPrefix(name, "nvidia-") || strings.HasPrefix(name, "libnvidia-")) && strings.Contains(name, "G07") {
			for _, v := range packages[name] {
				if upstreamVersion(v) != nvidiaIntegrationVersion {
					status.State = "inconsistent"
					status.Detail = name + " " + v
					return status, nil
				}
			}
		}
		if conflictingNvidiaPackage(name) {
			status.State = "conflict"
			status.Detail = name
			return status, nil
		}
	}
	count := 0
	for _, name := range nvidiaRequiredPackages {
		if len(packages[name]) > 0 {
			count++
		}
		for _, version := range packages[name] {
			if upstreamVersion(version) != nvidiaIntegrationVersion {
				status.State = "inconsistent"
				status.Detail = name + " " + version
				return status, nil
			}
		}
	}
	managed := len(packages["lyra-nvidia"]) > 0
	if managed && !containsVersion(packages["lyra-nvidia"], nvidiaIntegrationVersion) {
		status.State = "inconsistent"
		status.Detail = "lyra-nvidia"
		return status, nil
	}
	if (count > 0 || managed) && count != len(nvidiaRequiredPackages) {
		status.State = "inconsistent"
		return status, nil
	}
	status.Installed = count == len(nvidiaRequiredPackages)
	if status.SecureBoot == "unknown" {
		status.State = "unknown-secure-boot"
		return status, nil
	}
	kernel, err := m.run.Output("uname", "-r")
	if err != nil {
		return status, err
	}
	target, targetErr := m.run.Output("readlink", "-f", "/boot/vmlinuz")
	if filepath.Base(target) == "vmlinuz" {
		target = filepath.Base(filepath.Dir(target))
	} else {
		target = strings.TrimPrefix(filepath.Base(target), "vmlinuz-")
	}
	if targetErr != nil || !strings.HasSuffix(target, "-default") {
		status.State = "unknown-boot-kernel"
		return status, nil
	}
	status.Detail = "NVIDIA " + nvidiaIntegrationVersion + " · Kernel " + kernel
	if target != kernel {
		status.Detail += " · /boot/vmlinuz: " + target
	}
	if !status.Installed {
		// First installation is qualified only against the native kernel of the
		// published signed KMP. Weak-updates compatibility is checked after install.
		if kernel != "6.12.0-160100.4-default" || target != kernel {
			status.State = "kernel-missing"
			return status, nil
		}
		// A .run installation may have a module without any RPM ownership.
		if version, _ := m.run.Output("modinfo", "-k", kernel, "-F", "version", "nvidia"); version != "" {
			status.State = "conflict"
			status.Detail = "nvidia.ko without the official RPM stack"
			return status, nil
		}
		status.State = "available"
		return status, nil
	}
	for _, release := range uniqueStrings(kernel, target) {
		version, err := m.run.Output("modinfo", "-k", release, "-F", "version", "nvidia")
		if err != nil || version != nvidiaIntegrationVersion {
			status.State = "kernel-missing"
			return status, nil
		}
		signer, _ := m.run.Output("modinfo", "-k", release, "-F", "signer", "nvidia")
		if signer != "SUSE Linux Enterprise Secure Boot CA" {
			status.State = "unsigned-module"
			return status, nil
		}
		filename, err := m.run.Output("modinfo", "-k", release, "-F", "filename", "nvidia")
		if err != nil {
			status.State = "kernel-missing"
			return status, nil
		}
		// weak-updates may be a symlink: verify ownership of the real module.
		filename, err = m.run.Output("readlink", "-f", filename)
		if err != nil {
			status.State = "kernel-missing"
			return status, nil
		}
		owner, err := m.run.Output("rpm", "-qf", "--qf", "%{NAME}", filename)
		if err != nil || owner != nvidiaSignedKMP {
			status.State = "unsigned-module"
			return status, nil
		}
	}
	status.State = "reboot-required"
	status.RebootRequired = true
	loaded, err := os.ReadFile(m.sysPath("module/nvidia/version"))
	if err == nil && strings.TrimSpace(string(loaded)) == nvidiaIntegrationVersion {
		status.State = "driver-error"
		status.RebootRequired = false
		if m.driverActive() {
			status.State = "active"
		}
	}
	if !managed && status.State != "driver-error" {
		status.State = "unmanaged"
	}
	return status, nil
}

func containsVersion(versions []string, want string) bool {
	return len(versions) == 1 && versions[0] == want
}

func uniqueStrings(first, second string) []string {
	if first == second {
		return []string{first}
	}
	return []string{first, second}
}

func (m nvidiaManager) driverActive() bool {
	// Match every detected GPU by PCI address; one working GPU must not mask a
	// second device bound to nouveau or reporting another driver version.
	out, err := m.run.Output("nvidia-smi", "--query-gpu=pci.bus_id,driver_version", "--format=csv,noheader")
	if err != nil {
		return false
	}
	active := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(line, ",")
		if len(fields) != 2 || strings.TrimSpace(fields[1]) != nvidiaIntegrationVersion {
			return false
		}
		address := strings.ToLower(strings.TrimSpace(fields[0]))
		if len(address) == 16 {
			address = address[4:]
		}
		active[address] = true
	}
	out, err = m.run.Output("lspci", "-Dnd", "10de:")
	if err != nil {
		return false
	}
	count := 0
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, " 0300:") && !strings.Contains(line, " 0302:") {
			continue
		}
		address := strings.ToLower(strings.Fields(line)[0])
		driver, err := filepath.EvalSymlinks(m.sysPath("bus/pci/devices/" + address + "/driver"))
		if err != nil || filepath.Base(driver) != "nvidia" || !active[address] {
			return false
		}
		count++
	}
	return count > 0 && count == len(active)
}

func (m nvidiaManager) check() error {
	status, err := m.status()
	if err != nil {
		return err
	}
	if status.State != "active" {
		return fmt.Errorf("%s: %s", status.State, status.Detail)
	}
	// Public read: never reconcile or write suspend policy here. NVML and the
	// loaded/on-disk modules are checked; rendering/suspend require separate tests.
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

func (s *SoftwareService) CheckNvidia() (bool, string, *dbus.Error) {
	s.activity.Touch()
	if err := newNvidiaManager().check(); err != nil {
		return false, err.Error(), nil
	}
	return true, "NVIDIA 610.57.04: NVML, module version and SUSE KMP verified", nil
}

// ReconcileNvidiaSuspendPolicy applies the managed power guard at daemon
// startup only. Public diagnostics never mutate power policy.
func ReconcileNvidiaSuspendPolicy() error {
	manager := newNvidiaManager()
	version := manager.packageVersion(nvidiaSignedKMP)
	if version == "" {
		version = manager.packageVersion(nvidiaKMPMeta)
	}
	if version == "" {
		return nil
	} // Unknown RPM state is not proof that a quarantine is obsolete.
	_, err := manager.reconcileSuspendPolicy(version)
	return err
}
