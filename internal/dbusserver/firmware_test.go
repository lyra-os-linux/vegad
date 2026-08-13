package dbusserver

import (
	"fmt"
	"strings"
	"testing"
)

type fakeFirmwareRunner struct {
	outputs map[string]string
	errors  map[string]error
	calls   []string
}

func (f *fakeFirmwareRunner) Output(name string, args ...string) (string, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, key)
	return f.outputs[key], f.errors[key]
}
func (f *fakeFirmwareRunner) Run(name string, args ...string) error {
	_, err := f.Output(name, args...)
	return err
}

func TestNonFreeFirmwareIgnoresUnrelatedHardware(t *testing.T) {
	runner := &fakeFirmwareRunner{outputs: map[string]string{"lspci -Dn": "8086:1234", "lsusb": "8087:0026"}, errors: map[string]error{}}
	status, err := (firmwareManager{run: runner}).status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Detected || len(status.Packages) != 0 {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestNonFreeFirmwareOffersOnlyAvailableMatchingPackage(t *testing.T) {
	runner := &fakeFirmwareRunner{outputs: map[string]string{
		"lspci -Dn": "0000:03:00.0 0400: 4444:0016",
		"lsusb":     "",
		"zypper --no-refresh info --repo repo-non-oss ivtv-firmware": "Name : ivtv-firmware",
	}, errors: map[string]error{"rpm -q ivtv-firmware": fmt.Errorf("missing")}}
	status, err := (firmwareManager{run: runner}).status()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Detected || status.Installed || len(status.Packages) != 1 || status.Packages[0] != "ivtv-firmware" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestNonFreeFirmwareInstallPinsRepositoryAndCreatesSnapshot(t *testing.T) {
	runner := &fakeFirmwareRunner{outputs: map[string]string{
		"lspci -Dn": "0000:03:00.0 0400: 4444:0016",
		"lsusb":     "",
		"zypper --no-refresh info --repo repo-non-oss ivtv-firmware":                                                                         "Name : ivtv-firmware",
		"snapper -c root create --type single --read-only --description antes do firmware non-oss --cleanup-algorithm number --print-number": "7",
	}, errors: map[string]error{"rpm -q ivtv-firmware": fmt.Errorf("missing")}}
	_, err := (firmwareManager{run: runner}).install(func(uint32, string) {})
	if err != nil {
		t.Fatal(err)
	}
	want := "zypper --non-interactive install --from repo-non-oss --no-recommends ivtv-firmware"
	if !containsString(runner.calls, want) {
		t.Fatalf("repository-pinned install missing: %v", runner.calls)
	}
}
