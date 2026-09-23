package dbusserver

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/distro"
	"github.com/lyraos/vegad/internal/profile"
)

func TestIsLiveCmdline(t *testing.T) {
	for cmdline, want := range map[string]bool{
		"BOOT_IMAGE=/boot/vmlinuz root=live:CDLABEL=LyraOS rd.live.image quiet": true,
		"BOOT_IMAGE=/boot/vmlinuz rd.live.image":                                true,
		"BOOT_IMAGE=/boot/vmlinuz root=live:CDLABEL=LyraOS":                     true,
		"BOOT_IMAGE=/boot/vmlinuz root=UUID=1234 rootflags=subvol=@ quiet":      false,
		"BOOT_IMAGE=/boot/vmlinuz root=UUID=1234 rd.live.imagex":                false,
	} {
		if got := isLiveCmdline(cmdline); got != want {
			t.Errorf("isLiveCmdline(%q) = %v, want %v", cmdline, got, want)
		}
	}
}

func TestRunFirstUpdateJobSkipsWhenMarkerExists(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "first-update.done")
	if err := writeFirstUpdateMarker(marker); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VEGAD_FIRST_UPDATE_MARKER", marker)
	// A missing keyring would fail the import; returning nil proves the
	// marker short-circuits before any key, repository or package work.
	t.Setenv("VEGAD_TRUSTED_KEYRING", filepath.Join(t.TempDir(), "missing.asc"))
	if err := RunFirstUpdateJob(profile.Desktop); err != nil {
		t.Fatal(err)
	}
}

func TestWriteFirstUpdateMarkerCreatesParent(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "nested", "first-update.done")
	if err := writeFirstUpdateMarker(marker); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
}

func stubFirstUpdateRunning(t *testing.T, running bool) {
	t.Helper()
	previous := firstUpdateRunning
	firstUpdateRunning = func() bool { return running }
	t.Cleanup(func() { firstUpdateRunning = previous })
}

func exitError(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("/bin/sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != code {
		t.Fatalf("exit %d: got %v", code, err)
	}
	return err
}

func TestExplainFirstUpdateLockMapsZypperLockWhileRunning(t *testing.T) {
	stubFirstUpdateRunning(t, true)
	// Zypper errors reach dbusserver wrapped with context and output.
	locked := fmt.Errorf("zypper query: %w — System management is locked", exitError(t, zypperExitLocked))
	if got := explainFirstUpdateLock(locked); !errors.Is(got, errFirstUpdateInProgress) {
		t.Fatalf("lock while first update runs: got %v", got)
	}
	other := fmt.Errorf("zypper query: %w", exitError(t, 1))
	if got := explainFirstUpdateLock(other); got != other {
		t.Fatalf("non-lock failure must be unchanged, got %v", got)
	}
	plain := errors.New("origem desconhecida")
	if got := explainFirstUpdateLock(plain); got != plain {
		t.Fatalf("non-exit failure must be unchanged, got %v", got)
	}
	if got := explainFirstUpdateLock(nil); got != nil {
		t.Fatalf("nil must stay nil, got %v", got)
	}
}

func TestExplainFirstUpdateLockKeepsOtherLockHolders(t *testing.T) {
	// Another tool (YaST, a terminal zypper) holding the lock is not the
	// first-boot update; its own message is more accurate.
	stubFirstUpdateRunning(t, false)
	locked := fmt.Errorf("zypper: %w", exitError(t, zypperExitLocked))
	if got := explainFirstUpdateLock(locked); got != locked {
		t.Fatalf("got %v", got)
	}
}

func TestRequireFirstUpdateIdle(t *testing.T) {
	stubFirstUpdateRunning(t, false)
	if err := requireFirstUpdateIdle(); err != nil {
		t.Fatalf("idle: %v", err)
	}
	stubFirstUpdateRunning(t, true)
	err := requireFirstUpdateIdle()
	if err == nil || err.Name != BusName+".Error.FirstUpdateInProgress" {
		t.Fatalf("running: got %v", err)
	}
	if len(err.Body) != 1 || err.Body[0] != errFirstUpdateInProgress.Error() {
		t.Fatalf("body: %v", err.Body)
	}
}

func TestPackageQueryErrorName(t *testing.T) {
	stubFirstUpdateRunning(t, true)
	if err := packageQueryError(fmt.Errorf("zypper: %w", exitError(t, zypperExitLocked))); err.Name != BusName+".Error.FirstUpdateInProgress" {
		t.Fatalf("locked: %s", err.Name)
	}
	if err := packageQueryError(errors.New("rpm -q falhou")); err.Name != "org.freedesktop.DBus.Error.Failed" {
		t.Fatalf("other: %s", err.Name)
	}
}

func TestSoftwareTransactionsRefusedDuringFirstUpdate(t *testing.T) {
	stubFirstUpdateRunning(t, true)
	s := &SoftwareService{activity: &Activity{}}
	want := BusName + ".Error.FirstUpdateInProgress"
	calls := map[string]func() *dbus.Error{
		"Install":          func() *dbus.Error { _, err := s.Install(":1.1", "official", "vim"); return err },
		"Remove":           func() *dbus.Error { _, err := s.Remove(":1.1", "official", "vim"); return err },
		"UpdateAll":        func() *dbus.Error { _, err := s.UpdateAll(":1.1"); return err },
		"UpdateAllNative":  func() *dbus.Error { _, err := s.UpdateAllNative(":1.1"); return err },
		"UpdatePackage":    func() *dbus.Error { _, err := s.UpdatePackage(":1.1", "official", "vim"); return err },
		"ClearCache":       func() *dbus.Error { _, err := s.ClearCache(":1.1"); return err },
		"ClearNativeCache": func() *dbus.Error { _, err := s.ClearNativeCache(":1.1"); return err },
		"AddRepo":          func() *dbus.Error { _, err := s.AddRepo(":1.1", "x", "https://example.invalid"); return err },
		"TrustRepoKey":     func() *dbus.Error { _, err := s.TrustRepoKey(":1.1", "x", "ABCD"); return err },
		"SetRepoEnabled":   func() *dbus.Error { return s.SetRepoEnabled(":1.1", "x", true) },
	}
	for name, call := range calls {
		if err := call(); err == nil || err.Name != want {
			t.Errorf("%s: got %v, want %s before Polkit or a transaction", name, err, want)
		}
	}
}

// Embedding the real interface makes any accidental install/update call panic:
// preparation only supplies refresh and read operations.
type preparationBackend struct {
	distro.PackageBackend
	calls *[]string
	fail  string
}

func (p preparationBackend) SyncDatabase() error {
	*p.calls = append(*p.calls, "refresh")
	if p.fail == "refresh" {
		return errors.New("offline")
	}
	return nil
}

func (p preparationBackend) ListUpdates() ([]distro.PackageRef, error) {
	*p.calls = append(*p.calls, "list")
	if p.fail == "list" {
		return nil, errors.New("query failed")
	}
	return []distro.PackageRef{{Id: "vim"}, {Id: "kernel-default"}}, nil
}

func TestPrepareInitialRepositories(t *testing.T) {
	for _, fail := range []string{"", "import", "refresh", "list", "publish"} {
		t.Run("failure="+fail, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "done")
			t.Setenv("VEGAD_UPDATE_STATE", filepath.Join(t.TempDir(), "status.json"))
			if err := persistUpdateStatus(updateStatePath(), UpdateStatus{Profile: "desktop", FlatpakCount: 3}); err != nil {
				t.Fatal(err)
			}
			var calls []string
			backend := preparationBackend{calls: &calls, fail: fail}
			importKeys := func() error {
				calls = append(calls, "import")
				if fail == "import" {
					return errors.New("invalid keyring")
				}
				return nil
			}
			publish := func(status UpdateStatus) error {
				calls = append(calls, "publish")
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("marker written before publication")
				}
				if status.NativeCount != 2 || status.FlatpakCount != 3 || status.TotalCount != 5 {
					t.Fatalf("unexpected status: %+v", status)
				}
				if fail == "publish" {
					return errors.New("publication failed")
				}
				return nil
			}
			err := prepareInitialRepositories(profile.Desktop, marker, backend, importKeys, publish)
			if (err != nil) != (fail != "") {
				t.Fatalf("error: %v", err)
			}
			want := []string{"import", "refresh", "list", "publish"}
			for i, step := range want {
				if step == fail {
					want = want[:i+1]
					break
				}
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls: %v, want %v", calls, want)
			}
			_, markerErr := os.Stat(marker)
			if fail == "" && markerErr != nil {
				t.Fatal(markerErr)
			}
			if fail != "" && !os.IsNotExist(markerErr) {
				t.Fatalf("failed preparation marked done: %v", markerErr)
			}
		})
	}
}
