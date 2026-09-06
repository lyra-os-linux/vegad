package dbusserver

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestGrubRebuildFailureRestoresConfigurationAndMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grub")
	previous := "# original\nGRUB_TIMEOUT=3\n"
	if err := os.WriteFile(path, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Setxattr(path, "user.lyra-test", []byte("preserved"), 0); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("rebuild failed")
	err := applyGrubBootConfigAt(path, "saved", 5, "quiet", func() error { return failure })
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != previous {
		t.Fatalf("failed rebuild changed configuration: %q", got)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatal("mode changed")
	}
	value := make([]byte, 64)
	n, err := syscall.Getxattr(path, "user.lyra-test", value)
	if err != nil || string(value[:n]) != "preserved" {
		t.Fatalf("xattr lost: %q %v", value[:n], err)
	}
}

func TestGrubArgumentsRemainLiteralWhenSourced(t *testing.T) {
	for _, value := range []string{"quiet splash", `quiet $(printf evaluated)`, "quiet `printf evaluated`", `a="b" path=C:\test\ $HOME 'single'`, ""} {
		path := filepath.Join(t.TempDir(), "grub")
		if err := rewriteKeyValueFile(path, map[string]string{"GRUB_CMDLINE_LINUX_DEFAULT": quoteShell(value)}); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("/bin/sh", "-c", `. "$1"; printf '%s' "$GRUB_CMDLINE_LINUX_DEFAULT"`, "fixture", path)
		out, err := cmd.Output()
		if err != nil || string(out) != value {
			t.Fatalf("shell evaluated %q as %q: %v", value, out, err)
		}
		if decoded := unquoteShell(quoteShell(value)); decoded != value {
			t.Fatalf("read/write changed %q into %q", value, decoded)
		}
	}
}

func TestBootConfigRejectsLineInjectionWithoutChangingOriginal(t *testing.T) {
	for _, invalid := range []string{"quiet\nOTHER=value", "quiet\rvalue", "quiet\x00value"} {
		path := filepath.Join(t.TempDir(), "grub")
		original := "# keep\nGRUB_TIMEOUT=5\n"
		if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateBootValues(invalid); err == nil {
			t.Errorf("accepted %q", invalid)
		}
		if err := rewriteKeyValueFile(path, map[string]string{"GRUB_CMDLINE_LINUX_DEFAULT": quoteShell(invalid)}); err == nil {
			t.Fatal("writer accepted line injection")
		}
		got, _ := os.ReadFile(path)
		if string(got) != original {
			t.Fatalf("invalid write modified original: %q", got)
		}
	}
}

func TestSystemdBootMissingAndExistingKeysUseWhitespace(t *testing.T) {
	for _, original := range []string{"# loader\n", "default old.conf\ntimeout\t3\n", "default=old.conf\ntimeout=3\n"} {
		path := filepath.Join(t.TempDir(), "loader.conf")
		if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := rewriteConfigFile(path, " ", map[string]string{"default": "new.conf", "timeout": "5"}); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(path)
		if !strings.Contains(string(got), "default new.conf\n") || !strings.Contains(string(got), "timeout 5\n") || strings.Contains(string(got), "=") {
			t.Fatalf("invalid loader.conf: %s", got)
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o600 {
			t.Fatal("replacement changed permissions")
		}
	}
}

func TestBootWriterDoesNotFollowSymlinkOrOverwriteDirectory(t *testing.T) {
	base := t.TempDir()
	original := filepath.Join(base, "original")
	if err := os.WriteFile(original, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(original, link); err != nil {
		t.Fatal(err)
	}
	if err := rewriteKeyValueFile(link, map[string]string{"key": "value"}); err == nil {
		t.Fatal("symlink accepted")
	}
	got, _ := os.ReadFile(original)
	if string(got) != "keep" {
		t.Fatal("symlink target changed")
	}
	if err := rewriteKeyValueFile(base, map[string]string{"key": "value"}); err == nil {
		t.Fatal("directory accepted as config")
	}
}
