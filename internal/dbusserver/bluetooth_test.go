package dbusserver

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeHangingBluetoothctl puts a bluetoothctl on PATH that blocks like the
// real one does while bluetoothd is stopped.
func fakeHangingBluetoothctl(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\nexec sleep 30\n"
	if err := os.WriteFile(filepath.Join(bin, "bluetoothctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func withBluetoothSysfs(t *testing.T, controllers ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range controllers {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	previous := bluetoothSysfsPath
	bluetoothSysfsPath = dir
	t.Cleanup(func() { bluetoothSysfsPath = previous })
}

func TestBluetoothWithoutAdapterSkipsBluetoothctl(t *testing.T) {
	fakeHangingBluetoothctl(t)
	withBluetoothSysfs(t)
	b := &BluetoothService{activity: &Activity{}}

	start := time.Now()
	status, err := b.Status()
	if err != nil {
		t.Fatal(err)
	}
	devices, err := b.ListDevices()
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("queries without an adapter took %s, want immediate", elapsed)
	}
	if status.Available || status.Controller != "" {
		t.Fatalf("status = %+v, want unavailable", status)
	}
	if len(devices) != 0 {
		t.Fatalf("devices = %+v, want none", devices)
	}
}

func TestBluetoothQueryTimesOutWhenBluetoothdHangs(t *testing.T) {
	fakeHangingBluetoothctl(t)
	withBluetoothSysfs(t, "hci0")
	previous := bluetoothQueryTimeout
	bluetoothQueryTimeout = 200 * time.Millisecond
	t.Cleanup(func() { bluetoothQueryTimeout = previous })
	b := &BluetoothService{activity: &Activity{}}

	start := time.Now()
	status, err := b.Status()
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Status took %s, want it bounded by the query timeout", elapsed)
	}
	if !status.Available || status.Powered {
		t.Fatalf("status = %+v, want available and not powered", status)
	}
}
