package dbusserver

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestParentalEnrollmentAuthorizationPromptsOnceAndRechecksSilently(t *testing.T) {
	bin := t.TempDir()
	argsFile := filepath.Join(bin, "args")
	t.Setenv("PATH", bin)
	t.Setenv("VEGA_PARENTAL_POLKIT_ARGS", argsFile)
	t.Setenv("VEGA_PARENTAL_PROMPT_EXIT", "0")
	t.Setenv("VEGA_PARENTAL_RECHECK_EXIT", "0")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$VEGA_PARENTAL_POLKIT_ARGS\"\ncase \" $* \" in *' --allow-user-interaction '*) exit \"$VEGA_PARENTAL_PROMPT_EXIT\";; *) exit \"$VEGA_PARENTAL_RECHECK_EXIT\";; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "pkcheck"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	authorize, revalidate := parentalEnrollmentAuthorization(":1.73")
	allowed, err := authorize()
	if err != nil || !allowed {
		t.Fatalf("interactive authorization = %t, %v", allowed, err)
	}
	if err := revalidate(); err != nil {
		t.Fatalf("noninteractive revalidation failed: %v", err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected exactly a prompt and a recheck, got %q", data)
	}
	wantBase := parentalEnrollmentPolkitAction + " --system-bus-name :1.73"
	if !strings.Contains(lines[0], wantBase) || !strings.Contains(lines[0], "--allow-user-interaction") {
		t.Fatalf("initial check was not interactive and caller-bound: %q", lines[0])
	}
	if !strings.Contains(lines[1], wantBase) || strings.Contains(lines[1], "--allow-user-interaction") {
		t.Fatalf("recheck was not silent and caller-bound: %q", lines[1])
	}
}

func TestParentalEnrollmentAuthorizationDenialAndRevocationFailClosed(t *testing.T) {
	bin := t.TempDir()
	argsFile := filepath.Join(bin, "args")
	t.Setenv("PATH", bin)
	t.Setenv("VEGA_PARENTAL_POLKIT_ARGS", argsFile)
	t.Setenv("VEGA_PARENTAL_PROMPT_EXIT", "1")
	t.Setenv("VEGA_PARENTAL_RECHECK_EXIT", "1")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$VEGA_PARENTAL_POLKIT_ARGS\"\ncase \" $* \" in *' --allow-user-interaction '*) exit \"$VEGA_PARENTAL_PROMPT_EXIT\";; *) exit \"$VEGA_PARENTAL_RECHECK_EXIT\";; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "pkcheck"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	authorize, revalidate := parentalEnrollmentAuthorization(":1.99")
	allowed, err := authorize()
	if err == nil || allowed {
		t.Fatalf("denied prompt authorized enrollment: %t %v", allowed, err)
	}
	data, readErr := os.ReadFile(argsFile)
	if readErr != nil || strings.Contains(string(data), "--allow-user-interaction") == false {
		t.Fatalf("prompt denial did not invoke the interactive check: %q %v", data, readErr)
	}
	if err := revalidate(); err == nil {
		t.Fatal("revoked authorization passed silent revalidation")
	}
}

func TestParentalEnrollmentPolkitActionIsActiveAdminOnlyAndLocalized(t *testing.T) {
	data, err := os.ReadFile("../../packaging/org.lyraos.vega.policy")
	if err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Actions []struct {
			ID           string `xml:"id,attr"`
			Descriptions []struct {
				Language string `xml:"lang,attr"`
				Text     string `xml:",chardata"`
			} `xml:"description"`
			Messages []struct {
				Language string `xml:"lang,attr"`
				Text     string `xml:",chardata"`
			} `xml:"message"`
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
	for _, action := range policy.Actions {
		if action.ID != parentalEnrollmentPolkitAction {
			continue
		}
		if action.Defaults.Any != "no" || action.Defaults.Inactive != "no" || action.Defaults.Active != "auth_admin" {
			t.Fatalf("unsafe parental Polkit defaults: %#v", action.Defaults)
		}
		for _, locale := range []string{"pt_BR", "es"} {
			descriptionFound, messageFound := false, false
			for _, row := range action.Descriptions {
				descriptionFound = descriptionFound || row.Language == locale && strings.TrimSpace(row.Text) != ""
			}
			for _, row := range action.Messages {
				messageFound = messageFound || row.Language == locale && strings.TrimSpace(row.Text) != ""
			}
			if !descriptionFound || !messageFound {
				t.Errorf("missing localized description or message for %s", locale)
			}
		}
		return
	}
	t.Fatal("dedicated parental enrollment action missing")
}

func TestParentalEnrollmentAuthorizationRejectsMissingSender(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	authorize, revalidate := parentalEnrollmentAuthorization(dbus.Sender(""))
	if allowed, err := authorize(); err == nil || allowed {
		t.Fatalf("empty D-Bus sender authorized: %t %v", allowed, err)
	}
	if err := revalidate(); err == nil {
		t.Fatal("empty D-Bus sender passed revalidation")
	}
}
