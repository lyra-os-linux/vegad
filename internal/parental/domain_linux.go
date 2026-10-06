package parental

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// BtrfsSnapshotDomain qualifies ONLY a snapshot/restore of the configured single
// subvolume. The authority must be a distinct writable subvolume on the same
// filesystem. This does not qualify a Snapper configuration, recursive backup,
// whole-filesystem/VM rollback, or the completeness/freshness of an issuer.
// Scope and all ancestors are trusted service configuration; callers cannot
// choose them. No directories, mounts, subvolumes or records are created.
type BtrfsSnapshotDomain struct{ scope string }

func NewBtrfsSnapshotDomain(scope string) *BtrfsSnapshotDomain {
	return &BtrfsSnapshotDomain{scope: scope}
}

var filesystemUUIDLine = regexp.MustCompile(`^Label: .* uuid: ([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)

// boundedOutput refuses excessive output without retaining it in memory.
type boundedOutput struct {
	bytes.Buffer
	remaining int
}

func (w *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, io.ErrShortBuffer
	}
	w.remaining -= len(p)
	return w.Buffer.Write(p)
}
func domainQuery(ctx context.Context, f *os.File, args ...string) ([]byte, error) {
	args = append(args, "/proc/self/fd/3")
	if len(args) > 0 && args[0] == "property" {
		args = append(args, "ro")
	}
	cmd := exec.CommandContext(ctx, "/usr/sbin/btrfs", args...)
	cmd.ExtraFiles = []*os.File{f}
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/sbin:/usr/bin"}
	cmd.WaitDelay = 100 * time.Millisecond
	out := &boundedOutput{remaining: 4096}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if e := cmd.Run(); e != nil {
		return nil, RecoveryRequired
	}
	return out.Bytes(), nil
}
func domainTree(data []byte) (uint64, error) {
	s := string(data)
	if !strings.HasSuffix(s, "\n") {
		return 0, RecoveryRequired
	}
	s = strings.TrimSuffix(s, "\n")
	n, e := strconv.ParseUint(s, 10, 64)
	if e != nil || n < 5 || strconv.FormatUint(n, 10) != s {
		return 0, RecoveryRequired
	}
	return n, nil
}
func domainUUID(data []byte) (string, error) {
	first, _, ok := bytes.Cut(data, []byte("\n"))
	if !ok {
		return "", RecoveryRequired
	}
	match := filesystemUUIDLine.FindSubmatch(first)
	if len(match) != 2 || string(match[1]) == "00000000-0000-0000-0000-000000000000" {
		return "", RecoveryRequired
	}
	return string(match[1]), nil
}
func domainDirectory(path string, private bool) (*os.File, unix.Stat_t, error) {
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, unix.Stat_t{}, RecoveryRequired
	}
	f := os.NewFile(uintptr(fd), path)
	var s unix.Stat_t
	var fs unix.Statfs_t
	if unix.Fstat(fd, &s) != nil || unix.Fstatfs(fd, &fs) != nil || fs.Type != unix.BTRFS_SUPER_MAGIC || fs.Flags&unix.ST_RDONLY != 0 || s.Uid != 0 || s.Mode&0022 != 0 || s.Ino == 2 || (private && s.Mode&07777 != 0700) {
		f.Close()
		return nil, s, RecoveryRequired
	}
	return f, s, nil
}
func canonicalDomainPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}
func withinDomain(scope, path string) bool {
	rel, e := filepath.Rel(scope, path)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func domainNamespace() (unix.Stat_t, error) {
	var s unix.Stat_t
	e := unix.Stat("/proc/self/ns/mnt", &s)
	return s, e
}

func (d *BtrfsSnapshotDomain) Qualify(authority, catalog string) (fingerprint string, err error) {
	if d == nil || !canonicalDomainPath(d.scope) || !canonicalDomainPath(authority) || !canonicalDomainPath(catalog) || !withinDomain(d.scope, catalog) || authority == catalog {
		return "", RecoveryRequired
	}
	ns, e := domainNamespace()
	if e != nil {
		return "", RecoveryRequired
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	paths := []string{d.scope, catalog, authority}
	files := make([]*os.File, 0, 3)
	stats := make([]unix.Stat_t, 0, 3)
	defer func() {
		for _, f := range files {
			if e := f.Close(); e != nil && err == nil {
				fingerprint = ""
				err = RecoveryRequired
			}
		}
	}()
	ids := make([]uint64, 3)
	for i, path := range paths {
		f, s, e := domainDirectory(path, i != 0)
		if e != nil {
			return "", e
		}
		files = append(files, f)
		stats = append(stats, s)
		if i != 1 && s.Ino != 256 {
			return "", RecoveryRequired
		}
		data, e := domainQuery(ctx, f, "inspect-internal", "rootid")
		if e != nil {
			return "", e
		}
		ids[i], e = domainTree(data)
		if e != nil {
			return "", e
		}
	}
	if ids[0] != ids[1] || ids[0] == ids[2] {
		return "", RecoveryRequired
	}
	uuids := make([]string, 2)
	for j, i := range []int{0, 2} {
		data, e := domainQuery(ctx, files[i], "filesystem", "show", "--raw")
		if e != nil {
			return "", e
		}
		uuids[j], e = domainUUID(data)
		if e != nil {
			return "", e
		}
		data, e = domainQuery(ctx, files[i], "property", "get", "-t", "subvol")
		if e != nil || string(data) != "ro=false\n" {
			return "", RecoveryRequired
		}
	}
	if uuids[0] != uuids[1] || ctx.Err() != nil {
		return "", RecoveryRequired
	}
	// Re-opened path identities must still denote the held descriptors. A trusted
	// root changing topology is outside the hostile-root threat model, but an
	// observed replacement/namespace switch never yields a successful witness.
	for i, path := range paths {
		var after unix.Stat_t
		if unix.Lstat(path, &after) != nil || after.Dev != stats[i].Dev || after.Ino != stats[i].Ino || after.Mode != stats[i].Mode || after.Uid != stats[i].Uid {
			return "", Conflict
		}
	}
	afterNS, e := domainNamespace()
	if e != nil || afterNS.Dev != ns.Dev || afterNS.Ino != ns.Ino {
		return "", Conflict
	}
	witness := fmt.Sprintf("btrfs-single-subvolume-v1\n%q\n%q\n%q\n%s\n%d:%d\n%d:%d:%d\n%d:%d:%d\n%d:%d:%d\n", d.scope, catalog, authority, uuids[0], ns.Dev, ns.Ino, ids[0], stats[0].Dev, stats[0].Ino, ids[1], stats[1].Dev, stats[1].Ino, ids[2], stats[2].Dev, stats[2].Ino)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(witness))), nil
}
