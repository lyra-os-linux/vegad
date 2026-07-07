package dbusserver

import "testing"

func TestSearchFlatpakFindsFirefox(t *testing.T) {
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
