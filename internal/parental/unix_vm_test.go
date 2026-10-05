package parental

import (
	"context"
	"encoding/json"
	"golang.org/x/sys/unix"
	"os"
	"os/user"
	"strconv"
	"strings"
	"testing"
)

func vmGroup(name string) (uint32, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseUint(g.Gid, 10, 32)
	return uint32(v), err
}
func vmAccount(uid uint32) (UnixAccount, error) {
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return UnixAccount{}, err
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return UnixAccount{}, err
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		return UnixAccount{}, err
	}
	account := UnixAccount{Identity: Identity{User: u.Username, UID: uid, GID: uint32(gid), Group: g.Name}}
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return account, err
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) == 7 && fields[2] == u.Uid {
			count++
			if fields[0] != u.Username || fields[3] != u.Gid {
				return account, InvalidIdentity
			}
			account.FullName = fields[4]
		}
	}
	if count != 1 {
		return account, InvalidIdentity
	}
	data, err = os.ReadFile("/etc/group")
	if err != nil {
		return account, err
	}
	names := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) != 4 {
			continue
		}
		for _, name := range strings.Split(fields[3], ",") {
			if name == u.Username {
				if names[fields[0]] {
					return account, InvalidIdentity
				}
				names[fields[0]] = true
				gid, err := vmGroup(fields[0])
				if err != nil || strconv.FormatUint(uint64(gid), 10) != fields[2] {
					return account, InvalidIdentity
				}
				account.Supplementary = append(account.Supplementary, Group{fields[0], gid})
			}
		}
	}
	return account, nil
}
func TestUnixGuardDisposableVM(t *testing.T) {
	if os.Getenv("LYRA_PARENTAL_UNIX_VM") != "1" {
		t.Skip("requires guarded disposable VM and external account restoration")
	}
	cmd, err := os.ReadFile("/proc/cmdline")
	if err != nil || !strings.Contains(string(cmd), "lyra.parental-identity-test=1") {
		t.Fatal("wrong VM")
	}
	serial, err := os.ReadFile("/sys/block/vda/serial")
	if err != nil || strings.TrimSpace(string(serial)) != "lyra-admission-test" || os.Geteuid() != 0 {
		t.Fatal("wrong guest/disk/identity")
	}
	before, err := vmAccount(1002)
	if err != nil || before.Identity.User != "ordinaryuser" {
		t.Fatal("wrong target", err)
	}
	peer, err := vmAccount(1000)
	if err != nil || peer.Identity.User != "installeduser" {
		t.Fatal("wrong peer", err)
	}
	x := newFileFixture(t)
	// Bind fixture records to the actual local tuple; never change primary identity
	// to make the laboratory's default identity fit this VM.
	row := fixtureRow()
	first(row)["identity"] = before.Identity
	writePrivate(t, x.backend.catalog+"/catalog.json", encodeRow(t, row))
	writePrivate(t, x.backend.inventory+"/1002.json", encodeRow(t, inventoryRow(before.Identity)))
	proof, _ := json.Marshal(before.Identity)
	writePrivate(t, x.backend.ledger+"/1002", proof)
	x.planner.observeIdentity = func(uid uint32) (Identity, error) { a, err := vmAccount(uid); return a.Identity, err }
	g := NewGuard(x.planner, x.backend, vmGroup)
	adapter := NewUnixAdapter(vmAccount, vmGroup)
	realRun := adapter.run
	commands := []map[string]any{}
	checks := []string{}
	noops := 0
	adapter.run = func(ctx context.Context, path string, args []string) error {
		for _, lock := range x.locks {
			probe(t, lock, unix.LOCK_EX, true)
		}
		probe(t, x.locks[2], unix.LOCK_SH, true)
		err := realRun(ctx, path, args)
		commands = append(commands, map[string]any{"path": path, "argv": args, "success": err == nil})
		return err
	}
	execute := func(input Mutation) {
		p, err := g.Prepare(":1.20", strings.Repeat("a", 32), 1, input)
		if err != nil {
			t.Fatal(err)
		}
		result, err := g.Execute(":1.20", p.Identifier(), func(AuthorizationRequest) (bool, error) { assertReleased(t, x); return true, nil }, adapter.Apply)
		if err != nil || result != "OrdinaryMutationDispatched" {
			t.Fatal(result, err)
		}
		assertReleased(t, x)
		now, err := vmAccount(1002)
		if err != nil || now.Identity != before.Identity {
			t.Fatal("primary identity changed", err)
		}
		if other, err := vmAccount(1000); err != nil || other.Identity != peer.Identity || other.FullName != peer.FullName || !equalVMGroups(other.Supplementary, peer.Supplementary) {
			t.Fatal("peer changed", err)
		}
	}
	execute(Mutation{Method: UpdateUser, FullName: "Lyra Go $(literal)", Groups: []string{"users"}})
	checks = append(checks, "profile and supplementary replacement")
	execute(Mutation{Method: SetAdmin, IsAdmin: true})
	checks = append(checks, "grant wheel")
	count := len(commands)
	execute(Mutation{Method: SetAdmin, IsAdmin: true})
	if len(commands) != count {
		t.Fatal("repeat grant ran command")
	}
	noops++
	checks = append(checks, "repeat grant no-op")
	execute(Mutation{Method: SetAdmin})
	checks = append(checks, "revoke wheel")
	count = len(commands)
	execute(Mutation{Method: SetAdmin})
	if len(commands) != count {
		t.Fatal("repeat revoke ran command")
	}
	noops++
	checks = append(checks, "repeat revoke no-op")
	p, err := g.Prepare(":1.20", strings.Repeat("a", 32), 1, grantInput())
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.Execute(":1.20", p.Identifier(), func(AuthorizationRequest) (bool, error) { assertReleased(t, x); return false, nil }, adapter.Apply)
	if err != Denied || len(commands) != 3 {
		t.Fatal("denial dispatched", err)
	}
	checks = append(checks, "denied no command")
	_, err = g.Execute(":1.20", p.Identifier(), func(AuthorizationRequest) (bool, error) {
		assertReleased(t, x)
		writePrivate(t, x.backend.ledger+"/.deny-1002", []byte("fixture intent"))
		return true, nil
	}, adapter.Apply)
	if err != Blocked || len(commands) != 3 {
		t.Fatal("barrier dispatched", err)
	}
	assertReleased(t, x)
	checks = append(checks, "barrier no command")

	receipt := map[string]any{"passed": true, "checks": checks, "commands": commands, "noops": noops, "identity_preserved": true, "peer_preserved": true, "root_owned_backend": x.backend.owner == 0, "real_unix_effects": true, "real_polkit": false, "authoritative_pam_proof": false}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile("/root/lyra-go-unix-test/go-result.json", data, 0600); err != nil {
		t.Fatal(err)
	}
}
func equalVMGroups(a, b []Group) bool {
	m := map[string]uint32{}
	n := map[string]uint32{}
	for _, g := range a {
		m[g.Name] = g.GID
	}
	for _, g := range b {
		n[g.Name] = g.GID
	}
	return equalMemberships(m, n)
}
