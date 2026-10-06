package parental

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const snapperConfigLimit = 16 * 1024

var snapperAssignment = regexp.MustCompile(`^([A-Z][A-Z_]*)="([ -~]*)"$`)
var snapperKeys = strings.Fields("SUBVOLUME FSTYPE QGROUP SPACE_LIMIT FREE_LIMIT ALLOW_USERS ALLOW_GROUPS SYNC_ACL BACKGROUND_COMPARISON NUMBER_CLEANUP NUMBER_MIN_AGE NUMBER_LIMIT NUMBER_LIMIT_IMPORTANT TIMELINE_CREATE TIMELINE_CLEANUP TIMELINE_MIN_AGE TIMELINE_LIMIT_HOURLY TIMELINE_LIMIT_DAILY TIMELINE_LIMIT_WEEKLY TIMELINE_LIMIT_MONTHLY TIMELINE_LIMIT_QUARTERLY TIMELINE_LIMIT_YEARLY EMPTY_PRE_POST_CLEANUP EMPTY_PRE_POST_MIN_AGE COMPRESSION UNIQUE_NUMBERS")

// decodeSnapperScope supports a conservative subset of Snapper's sysconfig
// syntax. It never sources shell code. Unknown/duplicate keys and ambiguous
// quoting refuse. Numeric cleanup settings are opaque, not certified here.
func decodeSnapperScope(data []byte, scope string) error {
	if len(data) == 0 || !utf8.Valid(data) || len(data) > snapperConfigLimit || data[len(data)-1] != '\n' || !canonicalDomainPath(scope) {
		return RecoveryRequired
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.ContainsAny(line, "\r\x00") {
			return RecoveryRequired
		}
		line = strings.Trim(line, " \t")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := snapperAssignment.FindStringSubmatch(line)
		if len(m) != 3 || strings.ContainsAny(m[2], "\"\\$`#") {
			return RecoveryRequired
		}
		known := false
		for _, key := range snapperKeys {
			if key == m[1] {
				known = true
				break
			}
		}
		if !known {
			return RecoveryRequired
		}
		if _, ok := values[m[1]]; ok {
			return RecoveryRequired
		}
		values[m[1]] = m[2]
	}
	required := map[string]string{"SUBVOLUME": scope, "FSTYPE": "btrfs", "ALLOW_USERS": "", "ALLOW_GROUPS": "", "SYNC_ACL": "no"}
	for key, want := range required {
		if got, ok := values[key]; !ok || got != want {
			return RecoveryRequired
		}
	}
	for _, key := range strings.Fields("SYNC_ACL BACKGROUND_COMPARISON NUMBER_CLEANUP TIMELINE_CREATE TIMELINE_CLEANUP EMPTY_PRE_POST_CLEANUP UNIQUE_NUMBERS") {
		if value, ok := values[key]; ok && value != "yes" && value != "no" {
			return RecoveryRequired
		}
	}
	return nil
}

// SnapperSnapshotDomain binds a trusted, root-owned Snapper configuration to
// the single-subvolume Btrfs domain. It does not run Snapper, register a config,
// certify the boot/default-subvolume recipe, grant maintenance or provision data.
// All path ancestry and cooperating root config writers must be trusted.
type SnapperSnapshotDomain struct {
	config, scope string
	owner         uint32
	qualify       func(string, string) (string, error)
}

func NewSnapperSnapshotDomain(config, scope string) *SnapperSnapshotDomain {
	return &SnapperSnapshotDomain{config: config, scope: scope, owner: 0, qualify: NewBtrfsSnapshotDomain(scope).Qualify}
}
func safeSnapperFile(s unix.Stat_t, owner uint32) bool {
	return s.Mode&unix.S_IFMT == unix.S_IFREG && s.Uid == owner && s.Mode&0022 == 0 && s.Mode&07000 == 0 && s.Nlink == 1 && s.Size > 0 && s.Size <= snapperConfigLimit
}
func (s *SnapperSnapshotDomain) Qualify(authority, catalog string) (fingerprint string, err error) {
	if s == nil || s.qualify == nil || !canonicalDomainPath(s.config) || !canonicalDomainPath(s.scope) {
		return "", RecoveryRequired
	}
	dirPath, name := filepath.Split(s.config)
	dirPath = filepath.Clean(dirPath)
	d, e := unix.Open(dirPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if e != nil {
		return "", RecoveryRequired
	}
	defer func() {
		if unix.Close(d) != nil && err == nil {
			fingerprint = ""
			err = RecoveryRequired
		}
	}()
	var ds unix.Stat_t
	if unix.Fstat(d, &ds) != nil || ds.Uid != s.owner || ds.Mode&0022 != 0 {
		return "", RecoveryRequired
	}
	fd, e := unix.Openat(d, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return "", RecoveryRequired
	}
	f := os.NewFile(uintptr(fd), s.config)
	defer func() {
		if f.Close() != nil && err == nil {
			fingerprint = ""
			err = RecoveryRequired
		}
	}()
	var before unix.Stat_t
	if unix.Fstat(fd, &before) != nil || !safeSnapperFile(before, s.owner) {
		return "", RecoveryRequired
	}
	read := func() ([]byte, error) {
		if _, e := f.Seek(0, 0); e != nil {
			return nil, RecoveryRequired
		}
		data, e := io.ReadAll(io.LimitReader(f, snapperConfigLimit+1))
		if e != nil || len(data) != int(before.Size) {
			return nil, RecoveryRequired
		}
		return data, nil
	}
	data, e := read()
	if e != nil || decodeSnapperScope(data, s.scope) != nil {
		return "", RecoveryRequired
	}
	boundary, e := s.qualify(authority, catalog)
	if e != nil || !hash.MatchString(boundary) {
		return "", RecoveryRequired
	}
	afterData, e := read()
	var after, dirAfter, dirEntry unix.Stat_t
	if e != nil || !bytes.Equal(data, afterData) || unix.Fstat(fd, &after) != nil || !safeSnapperFile(after, s.owner) || after.Size != before.Size || after.Mtim != before.Mtim || after.Ctim != before.Ctim || !sameEntry(d, name, &before) || unix.Fstat(d, &dirAfter) != nil || dirAfter.Mode != ds.Mode || dirAfter.Uid != ds.Uid || unix.Lstat(dirPath, &dirEntry) != nil || dirEntry.Dev != ds.Dev || dirEntry.Ino != ds.Ino || dirEntry.Mode != ds.Mode || dirEntry.Uid != ds.Uid {
		return "", Conflict
	}
	witness := fmt.Sprintf("snapper-config-btrfs-single-subvolume-v1\n%q\n%q\n%d:%d\n%d:%d:%d\n%x\n%s\n", s.config, s.scope, ds.Dev, ds.Ino, before.Dev, before.Ino, before.Mode, sha256.Sum256(data), boundary)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(witness))), nil
}
