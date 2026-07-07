package dbusserver

import "testing"

func TestSearchPacmanFindsFirefox(t *testing.T) {
	results, err := searchPacman("firefox")
	if err != nil {
		t.Fatalf("searchPacman: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Origin != "official" {
			t.Fatalf("unexpected origin %q on result %+v", r.Origin, r)
		}
		if r.Id == "firefox" {
			found = true
			if r.Description == "" {
				t.Errorf("expected non-empty description for firefox, got %+v", r)
			}
		}
	}
	if !found {
		t.Fatalf("expected to find firefox in results, got %+v", results)
	}
}

func TestListPacmanReposIncludesCore(t *testing.T) {
	repos, err := listPacmanRepos()
	if err != nil {
		t.Fatalf("listPacmanRepos: %v", err)
	}
	found := false
	for _, r := range repos {
		if r == "options" {
			t.Fatalf("[options] section leaked into repo list: %+v", repos)
		}
		if r == "core" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected 'core' repo in %+v", repos)
	}
}
