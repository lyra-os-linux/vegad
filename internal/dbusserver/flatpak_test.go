package dbusserver

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSearchFlatpakFindsFirefox(t *testing.T) {
	if _, err := exec.LookPath("flatpak"); err != nil {
		t.Skip("flatpak é opcional e não está instalado")
	}
	remotes, err := exec.Command("flatpak", "remotes", "--system", "--columns=name").Output()
	if err != nil || !strings.Contains(string(remotes), "flathub") {
		t.Skip("o remote Flathub do sistema é necessário para este teste de integração")
	}

	results, err := searchFlatpak("firefox")
	if err != nil {
		t.Fatalf("searchFlatpak: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Origin != "flathub" {
			t.Fatalf("unexpected origin %q on result %+v", r.Origin, r)
		}
		if r.Id == "org.mozilla.firefox" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected to find org.mozilla.firefox in results, got %+v", results)
	}
}
