package dbusserver

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSyncPacmanDbSkipsWhenLockHeld(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "db.lck")
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("write fake lock: %v", err)
	}

	origPath, origDelay := pacmanLockPath, pacmanLockRetryDelay
	pacmanLockPath = lockPath
	pacmanLockRetryDelay = time.Millisecond
	defer func() {
		pacmanLockPath, pacmanLockRetryDelay = origPath, origDelay
	}()

	// Lock never goes away — syncPacmanDb should exhaust its retries and
	// return nil (skip this cycle) rather than an error, so the periodic
	// check job's systemd unit doesn't get marked as failed over a
	// transient race with another pacman transaction.
	if err := syncPacmanDb(); err != nil {
		t.Fatalf("expected syncPacmanDb to give up quietly on a held lock, got error: %v", err)
	}
}

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
