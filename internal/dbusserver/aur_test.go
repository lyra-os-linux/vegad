package dbusserver

import "testing"

func TestAurHelperDetection(t *testing.T) {
	helper, err := aurHelper()
	if !commandAvailable("paru") && !commandAvailable("yay") {
		if err == nil {
			t.Fatalf("expected error when no AUR helper is installed, got helper %q", helper)
		}
		t.Skip("no AUR helper (yay/paru) installed on this machine")
	}
	if err != nil {
		t.Fatalf("aurHelper: %v", err)
	}
	if helper != "paru" && helper != "yay" {
		t.Fatalf("unexpected helper %q", helper)
	}
}

func TestSearchAurFindsRealResult(t *testing.T) {
	if !commandAvailable("paru") && !commandAvailable("yay") {
		t.Skip("no AUR helper (yay/paru) installed on this machine")
	}

	results, err := searchAur("pkgtui")
	if err != nil {
		t.Fatalf("searchAur: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Origin != "aur" {
			t.Fatalf("unexpected origin %q on result %+v", r.Origin, r)
		}
		if r.Id == "pkgtui" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected to find pkgtui in AUR results, got %+v", results)
	}
}
