package dbusserver

import "testing"

func TestUfwRuleLineMatch(t *testing.T) {
	cases := []struct {
		line     string
		wantName string
		wantOK   bool
	}{
		{"OpenSSH                    ALLOW IN    Anywhere", "OpenSSH", true},
		{"OpenSSH (v6)               ALLOW IN    Anywhere (v6)", "OpenSSH", true},
		{"Apache Full                ALLOW IN    Anywhere", "Apache Full", true},
		{"22/tcp                     DENY IN     Anywhere", "22/tcp", true},
		{"--                         ------      ----", "", false},
		{"Status: active", "", false},
	}

	for _, tc := range cases {
		match := ufwRuleLine.FindStringSubmatch(tc.line)
		if !tc.wantOK {
			if match != nil {
				t.Errorf("line %q: expected no match, got %v", tc.line, match)
			}
			continue
		}
		if match == nil {
			t.Fatalf("line %q: expected a match, got none", tc.line)
		}
		got := match[1]
		if got != tc.wantName && got != tc.wantName+" (v6)" {
			t.Errorf("line %q: expected name %q, got %q", tc.line, tc.wantName, got)
		}
	}
}

func TestParseUfwStatusActive(t *testing.T) {
	if !parseUfwStatusActive("Status: active\n\nTo  Action  From\n") {
		t.Fatalf("expected active status to be detected")
	}
	if parseUfwStatusActive("Status: inactive\n") {
		t.Fatalf("expected inactive status to be detected as not active")
	}
}
