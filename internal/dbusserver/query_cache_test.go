package dbusserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A Flatpak updated outside vegad (by Vega in the user session, or in a
// terminal) must not leave a stale update list for five minutes.
func TestUpdateCacheFollowsFlatpakChanges(t *testing.T) {
	dir := t.TempDir()
	saved := flatpakSystemMarker
	flatpakSystemMarker = filepath.Join(dir, "system.changed")
	defer func() { flatpakSystemMarker = saved }()
	home := filepath.Join(dir, "home")
	userMarker := filepath.Join(home, ".local/share/flatpak/.changed")
	if err := os.MkdirAll(filepath.Dir(userMarker), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{flatpakSystemMarker, userMarker} {
		if err := os.WriteFile(marker, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	touch := func(path string, at time.Time) {
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().Add(-time.Hour)
	touch(flatpakSystemMarker, base)
	touch(userMarker, base)

	c := &queryCache{}
	key := queryCacheKey{1001, "ListUpdates"}
	reply := queryResponse{Values: []json.RawMessage{json.RawMessage(`[]`)}}
	stamp := flatpakStamp(home)
	_, _, gen := c.get(key, stamp)
	c.put(key, gen, stamp, reply)
	if _, ok, _ := c.get(key, flatpakStamp(home)); !ok {
		t.Fatal("unchanged Flatpak state must hit the cache")
	}
	touch(flatpakSystemMarker, base.Add(time.Minute))
	if _, ok, _ := c.get(key, flatpakStamp(home)); ok {
		t.Fatal("a system installation change must miss the cache")
	}
	stamp = flatpakStamp(home)
	c.put(key, gen, stamp, reply)
	touch(userMarker, base.Add(2*time.Minute))
	if _, ok, _ := c.get(key, flatpakStamp(home)); ok {
		t.Fatal("a user installation change must miss the cache")
	}
	if flatpakStamp("") == flatpakStamp(home) {
		t.Fatal("the user installation is part of the stamp")
	}
}
