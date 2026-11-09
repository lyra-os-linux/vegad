package dbusserver

import "testing"

// DeleteSnapshot(0) never reaches Polkit or snapper when Snapper is the
// backend; Timeshift ids are hashes, where 0 is just another id.
func TestRefuseCurrentSystem(t *testing.T) {
	if err := refuseCurrentSystem(0, true); err == nil || err.Name != "org.freedesktop.DBus.Error.InvalidArgs" {
		t.Fatalf("snapshot 0 with Snapper: %v", err)
	}
	for _, c := range []struct {
		id      uint32
		snapper bool
	}{{1, true}, {42, true}, {0, false}, {7, false}} {
		if err := refuseCurrentSystem(c.id, c.snapper); err != nil {
			t.Fatalf("id %d snapper %v refused: %v", c.id, c.snapper, err)
		}
	}
}
