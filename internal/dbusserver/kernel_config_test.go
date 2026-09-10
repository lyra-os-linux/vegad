package dbusserver

import (
	"encoding/json"
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

func TestSystemdBootConfigParsedByBackendVM(t *testing.T) {
	if os.Getenv("VEGA_BACKUP_VM_TEST") != "1" {
		t.Skip("requires disposable boot configuration VM")
	}
	if _, err := os.Stat("/run/vega-backup-test-vm"); err != nil || os.Geteuid() != 0 {
		t.Fatal("disposable VM marker/root missing")
	}
	base := t.TempDir()
	volume := filepath.Join(base, "boot.ext4")
	file, err := os.Create(volume)
	if err != nil {
		t.Fatal(err)
	}
	err = file.Truncate(64 << 20)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("mkfs.ext4", "-q", "-F", volume)
	device := run("losetup", "--find", "--show", volume)
	esp := filepath.Join(base, "esp")
	if err := os.Mkdir(esp, 0o755); err != nil {
		t.Fatal(err)
	}
	run("mount", "-t", "ext4", device, esp)
	t.Cleanup(func() { exec.Command("umount", esp).Run(); exec.Command("losetup", "--detach", device).Run() })
	t.Setenv("SYSTEMD_RELAX_ESP_CHECKS", "1")
	t.Setenv("SYSTEMD_RELAX_XBOOTLDR_CHECKS", "1")
	restoreFixture(t, esp, "vmlinuz", "configuration parser fixture")
	for _, id := range []string{"a", "z"} {
		restoreFixture(t, esp, "loader/entries/"+id+".conf", "title Test "+id+"\nlinux /vmlinuz\n")
	}
	for _, original := range []string{"# loader\n", "default z.conf\ntimeout 3\n", "default=z.conf\ntimeout=3\n"} {
		path := filepath.Join(esp, "loader/loader.conf")
		if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := rewriteConfigFile(path, " ", map[string]string{"default": "a.conf", "timeout": "5"}); err != nil {
			t.Fatal(err)
		}
		out := run("bootctl", "--esp-path="+esp, "--boot-path="+esp, "--no-variables", "--no-pager", "--json=short", "list")
		var entries []struct {
			ID      string `json:"id"`
			Default bool   `json:"isDefault"`
		}
		if err := json.Unmarshal([]byte(out), &entries); err != nil {
			t.Fatalf("bootctl output: %s: %v", out, err)
		}
		found := false
		for _, entry := range entries {
			if entry.ID == "a.conf" && entry.Default {
				found = true
			}
		}
		if !found {
			t.Fatalf("backend did not parse the configured default: %s", out)
		}
	}
}
