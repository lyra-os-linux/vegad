package dbusserver

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

// vegad#130: Export fails when the name belongs to another process.
func TestRequirePrimaryOwner(t *testing.T) {
	for _, reply := range []dbus.RequestNameReply{dbus.RequestNameReplyPrimaryOwner, dbus.RequestNameReplyAlreadyOwner} {
		if err := requirePrimaryOwner(reply); err != nil {
			t.Fatalf("reply %d refused: %v", reply, err)
		}
	}
	for _, reply := range []dbus.RequestNameReply{dbus.RequestNameReplyExists, dbus.RequestNameReplyInQueue} {
		if requirePrimaryOwner(reply) == nil {
			t.Fatalf("reply %d accepted", reply)
		}
	}
}
