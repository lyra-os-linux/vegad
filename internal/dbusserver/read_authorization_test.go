package dbusserver

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/profile"
)

func TestReadOnlyRoutingNeverPrompts(t *testing.T) {
	bin := t.TempDir()
	argsFile := filepath.Join(bin, "args")
	t.Setenv("PATH", bin)
	t.Setenv("VEGA_POLKIT_TEST_ARGS", argsFile)
	if err := os.WriteFile(filepath.Join(bin, "pkcheck"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$VEGA_POLKIT_TEST_ARGS\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	s := &Server{activity: &Activity{}, profile: profile.Desktop}
	count := 0
	for key, policy := range methodPolicies {
		if policy.privilege != privilegedRead {
			continue
		}
		count++
		t.Run(key, func(t *testing.T) {
			iface, name, _ := strings.Cut(key, ".")
			service := s.queryService(iface)
			if iface == "Backup" {
				service = &BackupService{activity: s.activity}
			}
			methods, err := s.routedMethods(service, BusName+"."+iface)
			if err != nil {
				t.Fatal(err)
			}
			fn := reflect.ValueOf(methods[name])
			args := make([]reflect.Value, fn.Type().NumIn())
			for i := range args {
				args[i] = reflect.Zero(fn.Type().In(i))
				if fn.Type().In(i) == senderType {
					args[i] = reflect.ValueOf(dbus.Sender(":1.42"))
				}
			}
			out := fn.Call(args)
			if out[len(out)-1].IsNil() {
				t.Fatal("denied read executed")
			}
			captured, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(captured), "--allow-user-interaction") {
				t.Fatal("read-only query could open an authentication dialog")
			}
			if !strings.Contains(string(captured), policy.action+"\n--system-bus-name\n:1.42\n") {
				t.Fatalf("lost original caller/action: %q", captured)
			}
		})
	}
	if count != 10 {
		t.Fatalf("expected ten brokered reads, got %d", count)
	}
	if requirePolkit(":1.42", "org.lyraos.vega.software.install") == nil {
		t.Fatal("denied administration authorized")
	}
	captured, err := os.ReadFile(argsFile)
	if err != nil || !strings.Contains(string(captured), "--allow-user-interaction") {
		t.Fatalf("explicit administration must retain authentication: %q %v", captured, err)
	}
}

func TestDesktopReadPolicyDoesNotAuthorizeMutations(t *testing.T) {
	data, err := os.ReadFile("../../packaging/org.lyraos.vega.policy")
	if err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Actions []struct {
			ID       string `xml:"id,attr"`
			Defaults struct {
				Any      string `xml:"allow_any"`
				Inactive string `xml:"allow_inactive"`
				Active   string `xml:"allow_active"`
			} `xml:"defaults"`
		} `xml:"action"`
	}
	if err := xml.Unmarshal(data, &policy); err != nil {
		t.Fatal(err)
	}
	readActions := map[string]bool{}
	for _, method := range methodPolicies {
		if method.privilege == privilegedRead {
			readActions[method.action] = true
		}
	}
	seen := 0
	for _, action := range policy.Actions {
		if readActions[action.ID] {
			seen++
			if action.Defaults.Active != "yes" || action.Defaults.Any != "no" || action.Defaults.Inactive != "no" {
				t.Errorf("desktop reads must be silent and limited to active sessions: %s", action.ID)
			}
		} else if action.Defaults.Active == "yes" || action.Defaults.Any == "yes" || action.Defaults.Inactive == "yes" {
			t.Errorf("administrative authorization was weakened: %s", action.ID)
		}
	}
	if seen != 4 {
		t.Fatalf("missing desktop read actions: %d", seen)
	}
}

func TestWifiListingDoesNotRequestScanAuthorization(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	stub := "#!/bin/sh\ncase \"$*\" in *'--rescan no') printf '%s\\n' '*:TestWifi:WPA2:72:wlan0';; *) exit 1;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "nmcli"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	service := &NetworkService{activity: &Activity{}}
	rows, err := service.ListWifi()
	if err != nil || len(rows) != 1 || rows[0].SSID != "TestWifi" || !rows[0].Active {
		t.Fatalf("listing must use existing scan data: %v %v", rows, err)
	}
}
