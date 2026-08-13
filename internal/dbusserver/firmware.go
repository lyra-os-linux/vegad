package dbusserver

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/godbus/dbus/v5"
)

// nonFreeFirmwareRule is a reviewed hardware-to-package association. Never
// infer proprietary firmware from a vendor name alone: installing unrelated
// firmware is useless and makes the security/licensing prompt misleading.
type nonFreeFirmwareRule struct {
	bus, hardwareID, packageName, description string
}

var nonFreeFirmwareRules = []nonFreeFirmwareRule{
	{"pci", "4444:", "ivtv-firmware", "Firmware para placas de captura Hauppauge/Conexant IVTV"},
	{"usb", "2cf0:", "bladeRF-fx3-firmware", "Firmware FX3 para rádio definido por software bladeRF"},
	{"usb", "2cf0:", "bladeRF-fpga-firmware", "Imagem FPGA para rádio definido por software bladeRF"},
}

type NonFreeFirmwareStatus struct {
	Detected  bool
	Installed bool
	Detail    string
	Packages  []string
}

type firmwareRunner interface {
	Output(name string, args ...string) (string, error)
	Run(name string, args ...string) error
}

type systemFirmwareRunner struct{}

func (systemFirmwareRunner) Output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("%s: %w%s", name, err, outputSuffix(text))
	}
	return text, nil
}

func (r systemFirmwareRunner) Run(name string, args ...string) error {
	_, err := r.Output(name, args...)
	return err
}

type firmwareManager struct{ run firmwareRunner }

func newFirmwareManager() firmwareManager { return firmwareManager{run: systemFirmwareRunner{}} }

func (m firmwareManager) inventory() (string, string, error) {
	pci, pciErr := m.run.Output("lspci", "-Dn")
	usb, usbErr := m.run.Output("lsusb")
	if pciErr != nil && usbErr != nil {
		return "", "", errors.New("não foi possível consultar o hardware PCI nem USB")
	}
	return strings.ToLower(pci), strings.ToLower(usb), nil
}

func (m firmwareManager) installed(packageName string) bool {
	_, err := m.run.Output("rpm", "-q", packageName)
	return err == nil
}

func (m firmwareManager) availableFromNonOSS(packageName string) bool {
	out, err := m.run.Output("zypper", "--no-refresh", "info", "--repo", "repo-non-oss", packageName)
	return err == nil && strings.Contains(strings.ToLower(out), strings.ToLower(packageName))
}

func (m firmwareManager) applicable() ([]nonFreeFirmwareRule, error) {
	pci, usb, err := m.inventory()
	if err != nil {
		return nil, err
	}
	var rules []nonFreeFirmwareRule
	for _, rule := range nonFreeFirmwareRules {
		inventory := pci
		if rule.bus == "usb" {
			inventory = usb
		}
		if strings.Contains(inventory, rule.hardwareID) && m.availableFromNonOSS(rule.packageName) {
			rules = append(rules, rule)
		}
	}
	return rules, nil
}

func (m firmwareManager) status() (NonFreeFirmwareStatus, error) {
	rules, err := m.applicable()
	if err != nil {
		return NonFreeFirmwareStatus{}, err
	}
	status := NonFreeFirmwareStatus{Detail: "Nenhum firmware não livre aplicável foi detectado."}
	if len(rules) == 0 {
		return status, nil
	}
	status.Detected = true
	var missing, installed []string
	for _, rule := range rules {
		status.Packages = append(status.Packages, rule.packageName)
		if m.installed(rule.packageName) {
			installed = append(installed, rule.description)
		} else {
			missing = append(missing, rule.description)
		}
	}
	status.Installed = len(missing) == 0
	if status.Installed {
		status.Detail = "Todo firmware não livre aplicável a este hardware já está instalado."
	} else {
		status.Detail = "Disponível no repositório non-oss: " + strings.Join(missing, "; ")
	}
	return status, nil
}

func (m firmwareManager) install(report progressFunc) (uint32, error) {
	status, err := m.status()
	if err != nil {
		return 0, err
	}
	if !status.Detected {
		return 0, errors.New("nenhum firmware não livre aplicável foi detectado")
	}
	if status.Installed {
		return 0, errors.New("o firmware não livre aplicável já está instalado")
	}
	var missing []string
	for _, packageName := range status.Packages {
		if !m.installed(packageName) {
			missing = append(missing, packageName)
		}
	}
	report(10, "Criando snapshot de recuperação")
	out, err := m.run.Output("snapper", "-c", "root", "create", "--type", "single", "--read-only", "--description", "antes do firmware non-oss", "--cleanup-algorithm", "number", "--print-number")
	if err != nil {
		return 0, fmt.Errorf("a instalação foi abortada porque o snapshot não pôde ser criado: %w", err)
	}
	var snapshot uint32
	if _, err := fmt.Sscan(strings.TrimSpace(out), &snapshot); err != nil || snapshot == 0 {
		return 0, fmt.Errorf("snapper retornou um identificador inválido %q", out)
	}
	report(45, "Instalando firmware selecionado do repositório non-oss")
	args := []string{"--non-interactive", "install", "--from", "repo-non-oss", "--no-recommends"}
	args = append(args, missing...)
	if err := m.run.Run("zypper", args...); err != nil {
		return snapshot, fmt.Errorf("a instalação do firmware falhou; use o snapshot %d para recuperação: %w", snapshot, err)
	}
	report(80, "Regenerando initramfs")
	if err := m.run.Run("dracut", "--force"); err != nil {
		return snapshot, fmt.Errorf("o initramfs não pôde ser regenerado; use o snapshot %d para recuperação: %w", snapshot, err)
	}
	report(100, fmt.Sprintf("Firmware instalado. Reinicie para ativá-lo. Snapshot de recuperação: %d", snapshot))
	return snapshot, nil
}

func (s *SoftwareService) NonFreeFirmwareStatus() (NonFreeFirmwareStatus, *dbus.Error) {
	s.activity.Touch()
	status, err := newFirmwareManager().status()
	if err != nil {
		return NonFreeFirmwareStatus{}, dbus.MakeFailedError(err)
	}
	return status, nil
}

func (s *SoftwareService) InstallNonFreeFirmware(sender dbus.Sender, confirmed bool) (uint32, *dbus.Error) {
	s.activity.Touch()
	if !confirmed {
		return 0, dbus.MakeFailedError(errors.New("confirmação explícita é obrigatória"))
	}
	if err := requirePolkit(sender, "org.lyraos.vega.software.install-firmware"); err != nil {
		return 0, err
	}
	return s.startTransaction("Instalação de firmware non-oss", func(report progressFunc, _ packageProgressFunc) error {
		_, err := newFirmwareManager().install(report)
		return err
	}), nil
}
