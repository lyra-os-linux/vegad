package distro

import (
	"os"
	"path/filepath"
	"testing"
)

func withOSRelease(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	saved := osReleasePath
	osReleasePath = path
	t.Cleanup(func() { osReleasePath = saved })
}

// vegad#132: vegad is exclusive to Lyra OS.
func TestDetectAcceptsOnlyLyraOS(t *testing.T) {
	accepted := map[string]string{
		"Lyra OS Desktop": "NAME=\"Lyra OS\"\nID=lyra-os\nID_LIKE=\"opensuse suse\"\nVERSION_ID=\"16.1\"\n",
		"Lyra OS Server":  "NAME=\"Lyra OS Server\"\nID=lyra-os-server\nID_LIKE=\"opensuse suse\"\nVERSION_ID=\"16.1\"\n",
	}
	for name, content := range accepted {
		withOSRelease(t, content)
		if id, err := Detect(); err != nil || id != OpenSUSELeap {
			t.Errorf("%s refused: %v %v", name, id, err)
		}
	}
	refused := map[string]string{
		"openSUSE Leap":       "NAME=\"openSUSE Leap\"\nID=\"opensuse-leap\"\nID_LIKE=\"suse opensuse\"\nVERSION_ID=\"16.1\"\n",
		"openSUSE Tumbleweed": "NAME=\"openSUSE Tumbleweed\"\nID=\"opensuse-tumbleweed\"\nID_LIKE=\"opensuse suse\"\n",
		"SLES":                "NAME=\"SLES\"\nID=\"sles\"\nID_LIKE=\"suse\"\nVERSION_ID=\"16.0\"\n",
		"derivative":          "ID=other\nID_LIKE=\"lyra-os opensuse suse\"\n",
		"empty":               "",
	}
	for name, content := range refused {
		withOSRelease(t, content)
		if id, err := Detect(); err == nil {
			t.Errorf("%s accepted as %v", name, id)
		}
	}
}
