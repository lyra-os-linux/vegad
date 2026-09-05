package dbusserver

import (
	"errors"
	"fmt"
	"os"
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
}

type systemFirmwareRunner struct{}

func (systemFirmwareRunner) Output(name string, args ...string) (string, error) {
	cmd := systemCommand(name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("%s: %w%s", name, err, outputSuffix(text))
	}
	return text, nil
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

func (s *SoftwareService) NonFreeFirmwareStatus() (NonFreeFirmwareStatus, *dbus.Error) {
	s.activity.Touch()
	status, err := newFirmwareManager().status()
	if err != nil {
		return NonFreeFirmwareStatus{}, dbus.MakeFailedError(err)
	}
	return status, nil
}

// InstallNonFreeFirmware keeps the v1 signature but no longer installs
// hardware packages or starts a transaction.
func (s *SoftwareService) InstallNonFreeFirmware(_ dbus.Sender, _ bool) (uint32, *dbus.Error) {
	return 0, driverManagementRemoved()
}
