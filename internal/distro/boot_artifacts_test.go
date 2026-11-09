package distro

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Installing or removing a kernel must not report success when the initrd
// or the GRUB menu could not be rebuilt.
func TestRebuildBootArtifactsFailsWhenAToolIsMissing(t *testing.T) {
	for _, present := range [][]string{{}, {"dracut"}, {"grub2-mkconfig"}} {
		dir := t.TempDir()
		for _, tool := range present {
			// A tool that would show it ran: it must never be reached.
			script := "#!/bin/sh\ntouch " + filepath.Join(dir, "ran-"+tool) + "\n"
			if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("PATH", dir)
		err := rebuildOpenSUSEBootArtifacts()
		if err == nil || !strings.Contains(err.Error(), "não encontrado") {
			t.Fatalf("present %v: err %v", present, err)
		}
		if matches, _ := filepath.Glob(filepath.Join(dir, "ran-*")); len(matches) > 0 {
			t.Fatalf("present %v: ran %v before checking every tool", present, matches)
		}
	}
}
