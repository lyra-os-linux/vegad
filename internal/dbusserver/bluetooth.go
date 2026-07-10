package dbusserver

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/godbus/dbus/v5"
)

type BluetoothService struct {
	activity *Activity
}

type BluetoothStatus struct {
	Available         bool
	Powered           bool
	Discoverable      bool
	Pairable          bool
	Scanning          bool
	Controller        string
	ControllerName    string
	TransferAvailable bool
	ReceiverActive    bool
	ReceivePath       string
}

type BluetoothDeviceInfo struct {
	Address   string
	Name      string
	Alias     string
	Icon      string
	Paired    bool
	Trusted   bool
	Connected bool
	Blocked   bool
	RSSI      int32
}

const bluetoothReceiverStatePath = "/tmp/vegad-bluetooth-receiver"

func (b *BluetoothService) Status() (BluetoothStatus, *dbus.Error) {
	b.activity.Touch()
	status := BluetoothStatus{
		Available:         commandAvailable("bluetoothctl"),
		TransferAvailable: commandAvailable("bt-obex"),
	}
	if !status.Available {
		return status, nil
	}
	out, err := runCommandOutput("bluetoothctl", "show")
	if err != nil {
		return status, nil
	}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := bluetoothInfoField(line)
		if !ok {
			continue
		}
		switch key {
		case "Controller":
			status.Controller = firstBluetoothToken(value)
		case "Name":
			status.ControllerName = value
		case "Powered":
			status.Powered = strings.EqualFold(value, "yes")
		case "Discoverable":
			status.Discoverable = strings.EqualFold(value, "yes")
		case "Pairable":
			status.Pairable = strings.EqualFold(value, "yes")
		case "Discovering":
			status.Scanning = strings.EqualFold(value, "yes")
		}
	}
	status.ReceiverActive, status.ReceivePath = bluetoothReceiverState()
	return status, nil
}

func (b *BluetoothService) ListDevices() ([]BluetoothDeviceInfo, *dbus.Error) {
	b.activity.Touch()
	if !commandAvailable("bluetoothctl") {
		return []BluetoothDeviceInfo{}, nil
	}
	out, err := runCommandOutput("bluetoothctl", "devices")
	if err != nil {
		return []BluetoothDeviceInfo{}, nil
	}
	var rows []BluetoothDeviceInfo
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "Device" || seen[fields[1]] {
			continue
		}
		seen[fields[1]] = true
		rows = append(rows, bluetoothDeviceInfo(fields[1]))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Connected != rows[j].Connected {
			return rows[i].Connected
		}
		if rows[i].Paired != rows[j].Paired {
			return rows[i].Paired
		}
		return strings.ToLower(rows[i].displayName()) < strings.ToLower(rows[j].displayName())
	})
	return rows, nil
}

func (b *BluetoothService) SetPowered(sender dbus.Sender, powered bool) *dbus.Error {
	b.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.bluetooth.configure"); err != nil {
		return err
	}
	return bluetoothctl("power", bluetoothOnOff(powered))
}

func (b *BluetoothService) SetDiscoverable(sender dbus.Sender, discoverable bool) *dbus.Error {
	b.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.bluetooth.configure"); err != nil {
		return err
	}
	return bluetoothctl("discoverable", bluetoothOnOff(discoverable))
}

func (b *BluetoothService) SetPairable(sender dbus.Sender, pairable bool) *dbus.Error {
	b.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.bluetooth.configure"); err != nil {
		return err
	}
	return bluetoothctl("pairable", bluetoothOnOff(pairable))
}

func (b *BluetoothService) SetScanning(sender dbus.Sender, scanning bool) *dbus.Error {
	b.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.bluetooth.configure"); err != nil {
		return err
	}
	return bluetoothctl("scan", bluetoothOnOff(scanning))
}

func (b *BluetoothService) Pair(sender dbus.Sender, address string) *dbus.Error {
	b.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.bluetooth.configure"); err != nil {
		return err
	}
	return bluetoothctl("pair", address)
}

func (b *BluetoothService) Trust(sender dbus.Sender, address string, trusted bool) *dbus.Error {
	b.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.bluetooth.configure"); err != nil {
		return err
	}
	if trusted {
		return bluetoothctl("trust", address)
	}
	return bluetoothctl("untrust", address)
}

func (b *BluetoothService) Connect(sender dbus.Sender, address string) *dbus.Error {
	b.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.bluetooth.configure"); err != nil {
		return err
	}
	return bluetoothctl("connect", address)
}

func (b *BluetoothService) Disconnect(sender dbus.Sender, address string) *dbus.Error {
	b.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.bluetooth.configure"); err != nil {
		return err
	}
	return bluetoothctl("disconnect", address)
}

func (b *BluetoothService) Remove(sender dbus.Sender, address string) *dbus.Error {
	b.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.bluetooth.configure"); err != nil {
		return err
	}
	return bluetoothctl("remove", address)
}

func (b *BluetoothService) SendFile(sender dbus.Sender, address string, path string) *dbus.Error {
	b.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.bluetooth.transfer"); err != nil {
		return err
	}
	if !commandAvailable("bt-obex") {
		return dbus.MakeFailedError(fmt.Errorf("bt-obex não está disponível"))
	}
	if _, err := os.Stat(path); err != nil {
		return dbus.MakeFailedError(err)
	}
	if err := runCommand("bt-obex", "-p", address, path); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (b *BluetoothService) StartFileReceiver(sender dbus.Sender, directory string) *dbus.Error {
	b.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.bluetooth.transfer"); err != nil {
		return err
	}
	if !commandAvailable("bt-obex") {
		return dbus.MakeFailedError(fmt.Errorf("bt-obex não está disponível"))
	}
	info, err := os.Stat(directory)
	if err != nil {
		return dbus.MakeFailedError(err)
	}
	if !info.IsDir() {
		return dbus.MakeFailedError(fmt.Errorf("%s não é uma pasta", directory))
	}
	cmd := exec.Command("bt-obex", "-s", directory)
	if err := cmd.Start(); err != nil {
		return dbus.MakeFailedError(err)
	}
	if err := os.WriteFile(bluetoothReceiverStatePath, []byte(fmt.Sprintf("%d\n%s\n", cmd.Process.Pid, directory)), 0o600); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (b *BluetoothDeviceInfo) displayName() string {
	if b.Alias != "" {
		return b.Alias
	}
	if b.Name != "" {
		return b.Name
	}
	return b.Address
}

func bluetoothDeviceInfo(address string) BluetoothDeviceInfo {
	info := BluetoothDeviceInfo{Address: address}
	out, err := runCommandOutput("bluetoothctl", "info", address)
	if err != nil {
		return info
	}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := bluetoothInfoField(line)
		if !ok {
			continue
		}
		switch key {
		case "Name":
			info.Name = value
		case "Alias":
			info.Alias = value
		case "Icon":
			info.Icon = value
		case "Paired":
			info.Paired = strings.EqualFold(value, "yes")
		case "Trusted":
			info.Trusted = strings.EqualFold(value, "yes")
		case "Connected":
			info.Connected = strings.EqualFold(value, "yes")
		case "Blocked":
			info.Blocked = strings.EqualFold(value, "yes")
		case "RSSI":
			rssi, _ := strconv.ParseInt(value, 10, 32)
			info.RSSI = int32(rssi)
		}
	}
	return info
}

func bluetoothctl(args ...string) *dbus.Error {
	if !commandAvailable("bluetoothctl") {
		return dbus.MakeFailedError(fmt.Errorf("bluetoothctl não está disponível"))
	}
	if err := runCommand("bluetoothctl", args...); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func bluetoothOnOff(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

func bluetoothInfoField(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", false
	}
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
}

func firstBluetoothToken(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func bluetoothReceiverState() (bool, string) {
	data, err := os.ReadFile(bluetoothReceiverStatePath)
	if err != nil {
		return false, ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		return false, ""
	}
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || pid <= 0 {
		return false, lines[1]
	}
	process, err := os.FindProcess(pid)
	if err != nil || process.Signal(syscall.Signal(0)) != nil {
		return false, lines[1]
	}
	return true, lines[1]
}
