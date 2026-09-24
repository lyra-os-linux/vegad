package dbusserver

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/profile"
)

type unclassifiedService struct{}

func (*unclassifiedService) NewOperation() *dbus.Error { return nil }

func TestQueryInventoryCoversEveryWireMethod(t *testing.T) {
	s := &Server{activity: &Activity{}, profile: profile.Desktop}
	services := map[string]interface{}{"Backup": &BackupService{activity: s.activity}}
	for _, name := range []string{"Metadata", "System", "Software", "Preparation", "Monitor", "Kernel", "Hardware", "Users", "Services", "Snapshots", "Firewall", "DateTime", "Network", "Storage", "Bluetooth", "Logs"} {
		services[name] = s.queryService(name)
	}
	seen := make(map[string]bool)
	wireInputs := func(t reflect.Type) []reflect.Type {
		var result []reflect.Type
		for i := 0; i < t.NumIn(); i++ {
			if t.In(i) != senderType {
				result = append(result, t.In(i))
			}
		}
		return result
	}
	for iface, service := range services {
		original := trackedMethods(service, s.activity)
		routed, err := s.routedMethods(service, BusName+"."+iface)
		if err != nil {
			t.Fatal(err)
		}
		for name, fn := range original {
			seen[iface+"."+name] = true
			before, after := reflect.TypeOf(fn), reflect.TypeOf(routed[name])
			if !reflect.DeepEqual(wireInputs(before), wireInputs(after)) || before.NumOut() != after.NumOut() {
				t.Fatalf("wire signature changed: %s.%s", iface, name)
			}
			for i := 0; i < before.NumOut(); i++ {
				if before.Out(i) != after.Out(i) {
					t.Fatalf("return signature changed: %s.%s", iface, name)
				}
			}
		}
	}
	for key := range methodPolicies {
		if !seen[key] {
			t.Errorf("stale policy: %s", key)
		}
	}
	if _, err := s.routedMethods(&unclassifiedService{}, BusName+".System"); err == nil {
		t.Fatal("unclassified operation exported")
	}
	t.Logf("%d classified methods preserve their D-Bus signatures", len(seen))
}

func TestQueryLauncherDropsPrivilegesAndDoesNotForwardRootEnvironment(t *testing.T) {
	t.Setenv("SECRET_TEST_TOKEN", "do-not-forward")
	t.Setenv("HOME", "/root")
	caller := &desktopUser{Uid: 1001, Gid: 1001, HomeDir: "/home/alice", RuntimeDir: "/run/user/1001"}
	for _, u := range []*desktopUser{caller, nil} {
		cmd, err := queryCommand(context.Background(), u, false, profile.Server)
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Join(cmd.Args, "\n")
		for _, required := range []string{"NoNewPrivileges=yes", "CapabilityBoundingSet=", "ProtectSystem=strict", "--setenv=VEGAD_PROFILE=server", "--expand-environment=no", "/usr/lib/vega/vegad\nquery"} {
			if !strings.Contains(args, required) {
				t.Errorf("missing %s", required)
			}
		}
		if strings.Contains(args, "/root") || strings.Contains(strings.Join(cmd.Env, "\n"), "SECRET_TEST_TOKEN") {
			t.Fatal("privileged environment leaked")
		}
		if u == nil && !strings.Contains(args, "User=vegad-query") {
			t.Fatal("root caller not demoted")
		}
		if u != nil && (!strings.Contains(args, "User=1001") || !strings.Contains(args, "Group=1001")) {
			t.Fatal("caller identity lost")
		}
		if strings.Contains(args, "systemd-journal") {
			t.Fatal("public query granted journal access")
		}
	}
	for _, u := range []*desktopUser{{Uid: 0, HomeDir: "/root"}, {Uid: 1001, HomeDir: "relative"}, {Uid: 1001, HomeDir: "/home/a\nUser=root"}} {
		if _, err := queryProperties(u, false); err == nil {
			t.Fatalf("accepted unsafe identity: %+v", u)
		}
	}
}

func TestQueryInputRejectsAdministrationBeforeConnecting(t *testing.T) {
	for _, request := range []string{`{"Interface":"Software","Method":"Install","Args":[]}`, `{"Interface":"Unknown","Method":"Query"}`, `{"Interface":"Kernel","Method":"BootStatus"}`, strings.Repeat("x", queryInputLimit+1), `{} {}`} {
		var out bytes.Buffer
		if err := RunQueryWorker(profile.Desktop, strings.NewReader(request), &out); err == nil || out.Len() != 0 {
			t.Fatalf("invalid input accepted: %v %q", err, out.String())
		}
	}
}

func TestBoundedQueryOutputDrainsWithoutUnboundedMemory(t *testing.T) {
	b := &boundedOutput{limit: 8}
	for _, chunk := range []string{"12345678", "90", strings.Repeat("x", 100)} {
		if n, err := b.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatal("must drain pipe")
		}
	}
	if !b.full || string(b.data) != "12345678" {
		t.Fatalf("limit failed: %+v", b)
	}
}

func TestQueryCacheIsolationInvalidationAndBounds(t *testing.T) {
	var c queryCache
	key := queryCacheKey{1001, "ListUpdates"}
	reply := queryResponse{Values: []json.RawMessage{json.RawMessage(`[]`)}}
	_, _, gen := c.get(key)
	c.put(key, gen, reply)
	if _, ok, _ := c.get(key); !ok {
		t.Fatal("successful response not cached")
	}
	if _, ok, _ := c.get(queryCacheKey{1002, "ListUpdates"}); ok {
		t.Fatal("other user's response leaked")
	}
	c.invalidate()
	c.put(key, gen, reply)
	if _, ok, _ := c.get(key); ok {
		t.Fatal("old in-flight query repopulated cache")
	}
	_, _, gen = c.get(key)
	c.put(key, gen, queryResponse{Error: dbus.MakeFailedError(os.ErrPermission)})
	if _, ok, _ := c.get(key); ok {
		t.Fatal("failure cached as valid update list")
	}
	c.put(key, gen, reply)
	e := c.entries[key]
	e.at = time.Now().Add(-nativeUpdateCacheTTL - time.Second)
	c.entries[key] = e
	if _, ok, _ := c.get(key); ok {
		t.Fatal("expired result served")
	}
	for uid := uint32(1001); uid < 1100; uid++ {
		c.put(queryCacheKey{uid, "ListUpdates"}, gen, reply)
	}
	if len(c.entries) > 16 {
		t.Fatal("unbounded cache")
	}
	key.uid = 5000
	c.put(key, gen, queryResponse{Values: []json.RawMessage{make([]byte, 2*1024*1024+1)}})
	if _, ok, _ := c.get(key); ok {
		t.Fatal("oversized response cached")
	}
}

func TestNativeQueryNeverWritesSharedUpdateState(t *testing.T) {
	svc, _ := newCachingService(t, profile.Server)
	state := []byte("existing administrative state")
	if err := os.WriteFile(updateStatePath(), state, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListNativeUpdates(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(updateStatePath())
	if err != nil || !bytes.Equal(data, state) {
		t.Fatalf("read query changed state: %q %v", data, err)
	}
}

func TestNvidiaWorkersCanReadModuleFilesWithoutModulePrivileges(t *testing.T) {
	caller := &desktopUser{Uid: 1001, Gid: 1001, HomeDir: "/home/alice", RuntimeDir: "/run/user/1001"}
	for _, tc := range []struct {
		iface, method string
		readModules   bool
	}{
		{"Software", "NvidiaStatus", true}, {"Software", "CheckNvidia", true},
		{"Software", "ListInstalled", false}, {"Hardware", "Inventory", false}, {"Unknown", "NvidiaStatus", false},
	} {
		cmd, err := queryCommand(context.Background(), caller, false, profile.Desktop, queryRequest{Interface: tc.iface, Method: tc.method})
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Join(cmd.Args, "\n")
		for _, required := range []string{"User=1001", "Group=1001", "NoNewPrivileges=yes", "CapabilityBoundingSet=", "ProtectSystem=strict"} {
			if !strings.Contains(args, required) {
				t.Fatalf("lost %s", required)
			}
		}
		if tc.readModules {
			if !strings.Contains(args, "ProtectKernelModules=no") || !strings.Contains(args, "SystemCallFilter=~@module") || strings.Contains(args, "ProtectKernelModules=yes") {
				t.Fatal(args)
			}
		} else if !strings.Contains(args, "ProtectKernelModules=yes") || strings.Contains(args, "ProtectKernelModules=no") {
			t.Fatal(args)
		}
	}
}
