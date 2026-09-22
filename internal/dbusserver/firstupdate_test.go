package dbusserver

import (
	"os"
	"path/filepath"
	"testing"

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
