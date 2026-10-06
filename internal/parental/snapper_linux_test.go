package parental

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func snapperTestConfig(scope string) []byte {
	return []byte("# explicit maintenance profile\nSUBVOLUME=\"" + scope + "\"\nFSTYPE=\"btrfs\"\nALLOW_USERS=\"\"\nALLOW_GROUPS=\"\"\nSYNC_ACL=\"no\"\nTIMELINE_CREATE=\"no\"\n")
}
func TestSnapperConfigConservativeScope(t *testing.T) {
	good := snapperTestConfig("/root")
	if e := decodeSnapperScope(good, "/root"); e != nil {
		t.Fatal(e)
	}
	for _, data := range [][]byte{nil, append([]byte("#\xff\n"), good...), bytes.Repeat([]byte("x"), snapperConfigLimit+1), bytes.TrimSuffix(good, []byte("\n")), append(append([]byte{}, good...), []byte("SUBVOLUME=\"/root\"\n")...), append(append([]byte{}, good...), []byte("UNKNOWN=\"x\"\n")...), bytes.ReplaceAll(good, []byte("/root"), []byte("/wrong")), bytes.ReplaceAll(good, []byte("btrfs"), []byte("ext4")), bytes.ReplaceAll(good, []byte("ALLOW_USERS=\"\""), []byte("ALLOW_USERS=\"child\"")), bytes.ReplaceAll(good, []byte("ALLOW_GROUPS=\"\""), []byte("ALLOW_GROUPS=\"wheel\"")), bytes.ReplaceAll(good, []byte("SYNC_ACL=\"no\""), []byte("SYNC_ACL=\"yes\"")), bytes.ReplaceAll(good, []byte("TIMELINE_CREATE=\"no\""), []byte("TIMELINE_CREATE=\"true\"")), bytes.ReplaceAll(good, []byte("/root"), []byte("$(id)")), bytes.ReplaceAll(good, []byte("/root"), []byte("/root\\/")), bytes.ReplaceAll(good, []byte("FSTYPE=\"btrfs\"\n"), nil), append(append([]byte{}, good...), []byte("source /etc/other\n")...), bytes.ReplaceAll(good, []byte("\n"), []byte("\r\n")), append(append([]byte{}, good...), 0, '\n')} {
		if e := decodeSnapperScope(data, "/root"); e == nil {
			t.Fatalf("accepted %q", data)
		}
	}
}
func TestSnapperConfigStorageBindingAndChanges(t *testing.T) {
	for _, mode := range []string{"good", "missing", "symlink", "hardlink", "fifo", "writable", "parent-writable", "duplicate", "changed", "replaced", "parent-replaced", "unqualified", "nil", "relative", "default-root"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "root")
			data := snapperTestConfig("/scope")
			writePrivate(t, path, data)
			s := NewSnapperSnapshotDomain(path, "/scope")
			s.owner = uint32(os.Geteuid())
			// Private topology fixture; actual positive Btrfs+Snapper needs root VM.
			s.qualify = func(string, string) (string, error) { return strings.Repeat("a", 64), nil }
			switch mode {
			case "missing":
				os.Remove(path)
			case "symlink":
				os.Rename(path, path+"-original")
				os.Symlink(path+"-original", path)
			case "hardlink":
				os.Link(path, path+"-copy")
			case "fifo":
				os.Remove(path)
				unix.Mkfifo(path, 0600)
			case "writable":
				os.Chmod(path, 0666)
			case "parent-writable":
				os.Chmod(dir, 0777)
			case "duplicate":
				writePrivate(t, path, append(data, []byte("FSTYPE=\"btrfs\"\n")...))
			case "changed":
				s.qualify = func(string, string) (string, error) {
					writePrivate(t, path, append(data, []byte("# changed\n")...))
					return strings.Repeat("a", 64), nil
				}
			case "replaced":
				s.qualify = func(string, string) (string, error) {
					os.Rename(path, path+"-original")
					writePrivate(t, path, data)
					return strings.Repeat("a", 64), nil
				}
			case "parent-replaced":
				s.qualify = func(string, string) (string, error) {
					os.Rename(dir, dir+"-old")
					os.Mkdir(dir, 0700)
					t.Cleanup(func() { os.RemoveAll(dir + "-old") })
					return strings.Repeat("a", 64), nil
				}
			case "unqualified":
				s.qualify = func(string, string) (string, error) { return "", RecoveryRequired }
			case "nil":
				s.qualify = nil
			case "relative":
				s.config = "relative"
			case "default-root":
				if os.Geteuid() == 0 {
					t.Skip("unprivileged test")
				}
				s = NewSnapperSnapshotDomain(path, "/scope")
			}
			witness, e := s.Qualify("/authority", "/scope/catalog")
			if mode == "good" {
				if e != nil || !hash.MatchString(witness) {
					t.Fatal(witness, e)
				}
				after, _ := os.ReadFile(path)
				if !bytes.Equal(data, after) {
					t.Fatal("modified config")
				}
				return
			}
			if e == nil || witness != "" {
				t.Fatal("accepted", mode, witness, e)
			}
		})
	}
}
