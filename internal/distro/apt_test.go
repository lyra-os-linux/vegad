package distro

import "testing"

func TestAptCacheSearchLine(t *testing.T) {
	match := aptCacheSearchLine.FindStringSubmatch("vim - Vi IMproved - enhanced vi editor")
	if match == nil {
		t.Fatalf("expected a match")
	}
	if match[1] != "vim" || match[2] != "Vi IMproved - enhanced vi editor" {
		t.Fatalf("unexpected match: %+v", match)
	}
}

func TestAptUpgradableLine(t *testing.T) {
	line := "curl/noble-updates 8.5.0-2ubuntu10.4 amd64 [upgradable from: 8.5.0-2ubuntu10.3]"
	match := aptUpgradableLine.FindStringSubmatch(line)
	if match == nil {
		t.Fatalf("expected a match")
	}
	if match[1] != "curl" || match[2] != "8.5.0-2ubuntu10.4" || match[3] != "8.5.0-2ubuntu10.3" {
		t.Fatalf("unexpected match: %+v", match)
	}
}

func TestParseAptCacheShowBlock(t *testing.T) {
	out := "Package: curl\n" +
		"Version: 8.5.0-2ubuntu10.4\n" +
		"Maintainer: Ubuntu Developers <ubuntu-devel-discuss@lists.ubuntu.com>\n" +
		"Homepage: https://curl.se\n" +
		"Size: 178190\n" +
		"Description: command line tool for transferring data with URL syntax\n" +
		"\n" +
		"Package: curl\n" +
		"Version: 8.5.0-2ubuntu10.3\n"

	fields := parseAptCacheShowBlock(out)
	if fields["Package"] != "curl" || fields["Version"] != "8.5.0-2ubuntu10.4" {
		t.Fatalf("unexpected first-stanza fields: %+v", fields)
	}
	if fields["Homepage"] != "https://curl.se" {
		t.Fatalf("expected Homepage to be parsed, got %+v", fields)
	}
}

func TestAptRepoLine(t *testing.T) {
	cases := []struct {
		line        string
		wantContent string
		wantEnabled bool
		wantIsRepo  bool
	}{
		{"deb http://archive.ubuntu.com/ubuntu noble main restricted", "deb http://archive.ubuntu.com/ubuntu noble main restricted", true, true},
		{"# deb http://archive.ubuntu.com/ubuntu noble main restricted", "deb http://archive.ubuntu.com/ubuntu noble main restricted", false, true},
		{"deb-src http://archive.ubuntu.com/ubuntu noble main", "deb-src http://archive.ubuntu.com/ubuntu noble main", true, true},
		{"# This is just a comment, not a repo line", "", false, false},
		{"", "", false, false},
	}

	for _, tc := range cases {
		content, enabled, isRepo := aptRepoLine(tc.line)
		if isRepo != tc.wantIsRepo {
			t.Errorf("line %q: expected isRepo=%v, got %v", tc.line, tc.wantIsRepo, isRepo)
			continue
		}
		if !isRepo {
			continue
		}
		if content != tc.wantContent || enabled != tc.wantEnabled {
			t.Errorf("line %q: expected (%q, %v), got (%q, %v)", tc.line, tc.wantContent, tc.wantEnabled, content, enabled)
		}
	}
}
