package dbusserver

import (
	"encoding/xml"
	"os"
	"testing"
)

type policyFile struct {
	Actions []struct {
		ID       string `xml:"id,attr"`
		Defaults struct {
			Any      string `xml:"allow_any"`
			Inactive string `xml:"allow_inactive"`
			Active   string `xml:"allow_active"`
		} `xml:"defaults"`
	} `xml:"action"`
}

// vegad#127: the polkit defaults behind Liberar espaço. Scan is a silent
// read only for the active local session; Clean asks the administrator once
// and keeps the authorization, never for inactive or remote sessions (the
// remote-admin rule handles those).
func TestStoragePolicyDefaults(t *testing.T) {
	data, err := os.ReadFile("../../packaging/org.lyraos.vega.policy")
	if err != nil {
		t.Fatal(err)
	}
	var policy policyFile
	if err := xml.Unmarshal(data, &policy); err != nil {
		t.Fatal(err)
	}
	want := map[string][3]string{
		"org.lyraos.vega.storage.read-admin": {"no", "no", "yes"},
		"org.lyraos.vega.storage.cleanup":    {"no", "no", "auth_admin_keep"},
	}
	for _, action := range policy.Actions {
		expected, ok := want[action.ID]
		if !ok {
			continue
		}
		got := [3]string{action.Defaults.Any, action.Defaults.Inactive, action.Defaults.Active}
		if got != expected {
			t.Errorf("%s: defaults %v, want %v (any, inactive, active)", action.ID, got, expected)
		}
		delete(want, action.ID)
	}
	for id := range want {
		t.Errorf("%s missing from the policy", id)
	}
}
