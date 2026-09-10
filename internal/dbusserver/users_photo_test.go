package dbusserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserPhotoPreservesAccountMetadataAndOtherSections(t *testing.T) {
	for _, existing := range []string{
		"# comment\n[User]\nLanguage=pt_BR.UTF-8\nSession=gnome\nSystemAccount=false\nIcon=/old\n[Other]\nIcon=other-icon\nCustom=keep\n",
		"[User]\nLanguage=pt_BR.UTF-8\n[Other]\nCustom=keep\n",
		"[Other]\nCustom=keep\n",
	} {
		root := t.TempDir()
		config := filepath.Join(root, "users", "alice")
		if err := os.MkdirAll(filepath.Dir(config), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config, []byte(existing), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := installUserPhotoAt(root, "alice", []byte("fixture-photo")); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(config)
		if !strings.Contains(string(data), "Icon="+filepath.Join(root, "icons", "alice")+"\n") {
			t.Fatal("icon missing")
		}
		for _, line := range strings.Split(existing, "\n") {
			if line != "Icon=/old" && line != "" && !strings.Contains(string(data), line+"\n") {
				t.Errorf("lost metadata %q from %s", line, data)
			}
		}
		info, _ := os.Stat(config)
		if info.Mode().Perm() != 0o600 {
			t.Fatal("account metadata permissions changed")
		}
	}
}

func TestUserPhotoFailureRetainsPreviousPhoto(t *testing.T) {
	root := t.TempDir()
	restoreFixture(t, root, "icons/alice", "old-photo")
	if err := os.MkdirAll(filepath.Join(root, "users"), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := filepath.Join(root, "original-account")
	if err := os.WriteFile(previous, []byte("[User]\nLanguage=pt_BR\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink can be read for preparation, but the atomic writer refuses
	// to replace it. That injects a config failure after the image is saved.
	if err := os.Symlink(previous, filepath.Join(root, "users", "alice")); err != nil {
		t.Fatal(err)
	}
	if err := installUserPhotoAt(root, "alice", []byte("new-photo")); err == nil {
		t.Fatal("expected configuration write failure")
	}
	assertRestoreFile(t, root, "icons/alice", "old-photo")
}
