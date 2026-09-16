// Package nvidiarecovery prepares a local, encrypted OS recovery point for
// NVIDIA first-install/adoption on the simple ext4 Server layout. Restoration
// is deliberately offline, against the original mounted filesystem only.
package nvidiarecovery

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const Base = "/var/lib/vegad-nvidia"
const dbPath = "/usr/lib/sysimage/rpm"

var roots = []string{"/usr", "/etc", "/boot", "/var/lib/alternatives"}
var referenceRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
var digestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Manifest struct {
	Schema    int      `json:"schema"`
	Reference string   `json:"reference"`
	Snapshot  string   `json:"snapshot"`
	UUID      string   `json:"root_uuid"`
	Machine   string   `json:"machine_sha256"`
	RPMHash   string   `json:"rpm_export_sha256"`
	Inventory []string `json:"inventory"`
	State     string   `json:"state"`
	Created   string   `json:"created"`
}

type Point struct {
	Directory string
	Manifest  Manifest
}

type mount struct {
	Target string `json:"target"`
	FSType string `json:"fstype"`
	UUID   string `json:"uuid"`
	Device string `json:"maj:min"`
}

func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, "/usr/bin/"+name, args...)
	c.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "HOME=/root"}
	c.Dir = "/"
	c.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	return c
}

func output(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := command(ctx, name, args...)
	b, err := c.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return b, nil
}

func mountAt(path string) (mount, error) {
	b, err := output("findmnt", "--json", "--target", path, "--output", "TARGET,FSTYPE,UUID,MAJ:MIN")
	if err != nil {
		return mount{}, err
	}
	var value struct {
		Filesystems []mount `json:"filesystems"`
	}
	if err = json.Unmarshal(b, &value); err != nil || len(value.Filesystems) != 1 {
		return mount{}, errors.New("ambiguous recovery mount")
	}
	return value.Filesystems[0], nil
}

func mountList() ([]mount, error) {
	b, err := output("findmnt", "--json", "--list", "--output", "TARGET,FSTYPE,UUID,MAJ:MIN")
	if err != nil {
		return nil, err
	}
	var value struct {
		Filesystems []mount `json:"filesystems"`
	}
	if err := json.Unmarshal(b, &value); err != nil {
		return nil, err
	}
	return value.Filesystems, nil
}

func protectedSourceMount(path string) bool {
	if path == "/boot/efi" {
		return false
	}
	for _, root := range append(append([]string{}, roots...), Base) {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

func owned(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 || info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory || info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("unsafe recovery path: %s", path)
	}
	if !directory && !info.Mode().IsRegular() {
		return errors.New("recovery file is not regular")
	}
	return nil
}

func private(path string, directory bool) error {
	if err := owned(path, directory); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("recovery secret is not root-only: %s", path)
	}
	return nil
}

func directory(path string, mode os.FileMode) error {
	if err := os.Mkdir(path, mode); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return owned(path, true)
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func atomicJSON(path string, value any, mode os.FileMode) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicFile(path, append(b, '\n'), mode)
}

func atomicFile(path string, b []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".recovery-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Preflight is read-only and may be used by the public diagnostic worker.
// The actual preparation repeats all checks with the package lock held.
func Preflight() error {
	for _, name := range []string{"restic", "rpmdb", "rpm", "findmnt", "du"} {
		if st, err := os.Stat("/usr/bin/" + name); err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("required recovery tool unavailable: %s", name)
		}
	}
	root, err := mountAt("/")
	if err != nil {
		return err
	}
	if root.Target != "/" || root.FSType != "ext4" || root.UUID == "" || root.Device == "" {
		return errors.New("NVIDIA recovery requires a simple ext4 root with a filesystem UUID")
	}
	for _, path := range append(append([]string{}, roots...), "/var", dbPath) {
		actual, err := filepath.EvalSymlinks(path)
		if err != nil || actual != path {
			return fmt.Errorf("unsupported recovery directory: %s", path)
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("recovery directory missing: %s", path)
		}
		m, err := mountAt(path)
		if err != nil || m.Device != root.Device {
			return fmt.Errorf("separate filesystem requires recovery qualification: %s", path)
		}
	}
	b, err := output("rpm", "--eval", "%{_dbpath}")
	if err != nil || strings.TrimSpace(string(b)) != dbPath {
		return errors.New("unsupported RPM database layout")
	}
	if os.Geteuid() == 0 {
		// Query workers have their own read-only bind mounts. The privileged
		// pre-commit check examines the real installation namespace instead.
		mounts, err := mountList()
		if err != nil {
			return err
		}
		for _, m := range mounts {
			if protectedSourceMount(m.Target) {
				return fmt.Errorf("unsupported mount inside recovery scope: %s", m.Target)
			}
		}
	}
	return nil
}

func inventory(target string) ([]string, error) {
	args := []string{}
	if target != "" {
		args = append(args, "--root", target)
	}
	args = append(args, "-qa", "--qf", "%{NAME}|%{EPOCHNUM}|%{VERSION}|%{RELEASE}|%{ARCH}\n")
	b, err := output("rpm", args...)
	if err != nil {
		return nil, err
	}
	rows := strings.Split(strings.TrimSpace(string(b)), "\n")
	sort.Strings(rows)
	if len(rows) < 2 {
		return nil, errors.New("empty RPM recovery inventory")
	}
	return rows, nil
}

func spaceAvailable(path string, required uint64) error {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return err
	}
	if uint64(fs.Bavail)*uint64(fs.Bsize) < required {
		return errors.New("insufficient free space for the NVIDIA recovery point")
	}
	return nil
}

func estimate() (uint64, error) {
	args := append([]string{"--summarize", "--apparent-size", "--block-size=1", "--one-file-system", "--"}, roots...)
	b, err := output("du", args...)
	if err != nil {
		return 0, err
	}
	var total uint64
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != len(roots) {
		return 0, errors.New("incomplete recovery size estimate")
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, errors.New("invalid recovery size")
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil || n > 1<<50 {
			return 0, errors.New("invalid recovery size")
		}
		total += n
	}
	// Conservative uncompressed size, metadata headroom and RPM/download space.
	return total + total/4 + 2*1024*1024*1024, nil
}

func (p Point) restic(report func(), args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	all := []string{"--repo", filepath.Join(p.Directory, "repository"), "--password-file", filepath.Join(p.Directory, "password"), "--no-cache"}
	c := command(ctx, "restic", append(all, args...)...)
	// Keep progress alive without exposing backup paths or credentials publicly.
	done := make(chan struct{})
	if report != nil {
		go func() {
			tick := time.NewTicker(5 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-tick.C:
					report()
				case <-done:
					return
				}
			}
		}()
	}
	var log tailBuffer
	c.Stdout = &log
	c.Stderr = &log
	err := c.Run()
	close(done)
	if err != nil {
		// Keep detailed filenames/output private; public transaction errors must
		// not disclose confidential OS backup contents.
		_ = atomicFile(filepath.Join(p.Directory, "last-error.txt"), log.data, 0600)
		return nil, fmt.Errorf("Restic recovery operation %s failed: %w", args[0], err)
	}
	return log.data, nil
}

// Restic emits periodic JSON progress. Keep a bounded tail containing its
// final summary instead of accumulating a log proportional to backup size.
type tailBuffer struct{ data []byte }

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	const limit = 256 * 1024
	if len(p) >= limit {
		b.data = append(b.data[:0], p[len(p)-limit:]...)
		return n, nil
	}
	if drop := len(b.data) + len(p) - limit; drop > 0 {
		b.data = b.data[drop:]
	}
	b.data = append(b.data, p...)
	return n, nil
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("recovery metadata exceeds its size limit")
	}
	return b, err
}

// Prepare must run inside the reviewed Zypper transaction's pre-commit callback.
// No fallback to an unverified or partial backup is permitted.
func Prepare(report func()) (*Point, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("root required for recovery preparation")
	}
	if err := Preflight(); err != nil {
		return nil, err
	}
	if RecoveryRequired() {
		return nil, errors.New("complete the interrupted offline recovery before installing NVIDIA")
	}
	if err := owned("/var/lib", true); err != nil {
		return nil, err
	}
	if err := directory(Base, 0755); err != nil {
		return nil, err
	}
	points := filepath.Join(Base, "points")
	if err := directory(points, 0700); err != nil {
		return nil, err
	}
	// Confidential OS configurations and the encryption key must remain root-only.
	if err := os.Chmod(points, 0700); err != nil {
		return nil, err
	}
	size, err := estimate()
	if err != nil {
		return nil, err
	}
	if err = spaceAvailable(points, size); err != nil {
		return nil, err
	}
	root, err := mountAt("/")
	if err != nil {
		return nil, err
	}
	machine, err := os.ReadFile("/etc/machine-id")
	if err != nil || len(strings.TrimSpace(string(machine))) != 32 {
		return nil, errors.New("machine identity unavailable")
	}
	before, err := inventory("")
	if err != nil {
		return nil, err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(random[:])
	dir := filepath.Join(points, id)
	if err = os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	p := &Point{Directory: dir, Manifest: Manifest{Schema: 1, Reference: id, UUID: root.UUID, Machine: digest(machine), Inventory: before, State: "preparing", Created: time.Now().UTC().Format(time.RFC3339)}}
	failed := true
	defer func() {
		if failed {
			_ = p.Mark("incomplete")
		}
	}()
	var password [32]byte
	if _, err = rand.Read(password[:]); err != nil {
		return nil, err
	}
	if err = atomicFile(filepath.Join(dir, "password"), []byte(hex.EncodeToString(password[:])), 0600); err != nil {
		return nil, err
	}
	// Portable, consistent header export; never restore a live SQLite/WAL copy.
	headers, err := output("rpmdb", "--exportdb")
	if err != nil {
		return nil, err
	}
	if len(headers) == 0 {
		return nil, errors.New("empty RPM header export")
	}
	p.Manifest.RPMHash = digest(headers)
	if err = atomicFile(filepath.Join(dir, "rpm-headers"), headers, 0600); err != nil {
		return nil, err
	}
	if _, err = p.restic(report, "init", "--repository-version", "2"); err != nil {
		return nil, err
	}
	args := []string{"backup", "--json", "--one-file-system", "--exclude", "/boot/efi", "--exclude", dbPath, "--tag", "lyra-nvidia-recovery", "--"}
	b, err := p.restic(report, append(args, roots...)...)
	if err != nil {
		return nil, err
	}
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		var summary struct {
			Type string `json:"message_type"`
			ID   string `json:"snapshot_id"`
		}
		if json.Unmarshal(line, &summary) == nil && summary.Type == "summary" {
			p.Manifest.Snapshot = summary.ID
		}
	}
	if !digestRE.MatchString(p.Manifest.Snapshot) {
		return nil, errors.New("Restic did not return a full recovery snapshot ID")
	}
	if _, err = p.restic(report, "check", "--read-data"); err != nil {
		return nil, err
	}
	after, err := inventory("")
	if err != nil || !equalInventory(before, after) {
		return nil, errors.New("RPM inventory changed while preparing recovery")
	}
	if err = p.Mark("ready"); err != nil {
		return nil, err
	}
	failed = false
	return p, nil
}

func equalInventory(a, b []string) bool { return strings.Join(a, "\n") == strings.Join(b, "\n") }

func (p *Point) Mark(state string) error {
	switch state {
	case "ready", "incomplete", "installed", "failed", "restoring", "restored":
	default:
		return errors.New("invalid recovery state")
	}
	p.Manifest.State = state
	return atomicJSON(filepath.Join(p.Directory, "manifest.json"), p.Manifest, 0600)
}

func load(target, reference string) (*Point, error) {
	if !referenceRE.MatchString(reference) {
		return nil, errors.New("invalid recovery reference")
	}
	dir := filepath.Join(target, Base, "points", reference)
	for _, path := range []string{filepath.Join(target, "var"), filepath.Join(target, "var/lib"), filepath.Join(target, Base), filepath.Join(target, Base, "points"), dir} {
		if err := owned(path, true); err != nil {
			return nil, err
		}
	}
	for _, path := range []string{filepath.Join(target, Base, "points"), dir} {
		if err := private(path, true); err != nil {
			return nil, err
		}
	}
	for _, name := range []string{"manifest.json", "password", "rpm-headers"} {
		if err := private(filepath.Join(dir, name), false); err != nil {
			return nil, err
		}
	}
	if err := owned(filepath.Join(dir, "repository"), true); err != nil {
		return nil, err
	}
	b, err := readLimited(filepath.Join(dir, "manifest.json"), 2*1024*1024)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err = json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.Schema != 1 || m.Reference != reference || !digestRE.MatchString(m.Snapshot) || !digestRE.MatchString(m.RPMHash) || !digestRE.MatchString(m.Machine) || m.UUID == "" || len(m.Inventory) < 2 {
		return nil, errors.New("invalid recovery manifest")
	}
	switch m.State {
	case "ready", "installed", "failed", "restoring", "restored":
	default:
		return nil, errors.New("recovery point was not verified")
	}
	headers, err := readLimited(filepath.Join(dir, "rpm-headers"), 512*1024*1024)
	if err != nil || digest(headers) != m.RPMHash {
		return nil, errors.New("RPM recovery export corrupted")
	}
	return &Point{Directory: dir, Manifest: m}, nil
}

// RestoreOffline requires an already mounted original ext4 root in a rescue OS.
// It does not mount, reformat, select disks, restore a running root, or touch ESP.
func RestoreOffline(target, reference string, confirmed bool) error {
	if os.Geteuid() != 0 {
		return errors.New("root required in the recovery environment")
	}
	if !confirmed {
		return errors.New("review the offline recovery target and confirm explicitly")
	}
	if !filepath.IsAbs(target) || filepath.Clean(target) != target || target == "/" {
		return errors.New("an absolute mounted offline root is required")
	}
	real, err := filepath.EvalSymlinks(target)
	if err != nil || real != target {
		return errors.New("recovery target contains a symlink")
	}
	current, err := mountAt("/")
	if err != nil {
		return err
	}
	dest, err := mountAt(target)
	if err != nil {
		return err
	}
	if dest.Target != target || dest.FSType != "ext4" || dest.Device == current.Device || dest.UUID == "" {
		return errors.New("target is not an offline ext4 filesystem")
	}
	// No subordinate mounts: this narrowly supports the default simple Server
	// layout. In rescue mode /proc,/sys,/dev and ESP must not be bind-mounted here.
	mounts, err := mountList()
	if err != nil {
		return err
	}
	for _, m := range mounts {
		if strings.HasPrefix(m.Target, target+"/") {
			return errors.New("unmount subordinate filesystems before NVIDIA recovery")
		}
		if m.Device == dest.Device && m.Target != target {
			return errors.New("recovery filesystem is mounted at another location")
		}
	}
	for _, path := range append(append([]string{}, roots...), "/var", dbPath) {
		joined := filepath.Join(target, path)
		actual, e := filepath.EvalSymlinks(joined)
		if e != nil || actual != joined {
			return fmt.Errorf("unsafe offline directory: %s", path)
		}
		if err := owned(joined, true); err != nil {
			return err
		}
	}
	for _, path := range []string{target, filepath.Join(target, "usr/lib"), filepath.Join(target, "usr/lib/sysimage")} {
		if err := owned(path, true); err != nil {
			return err
		}
	}
	p, err := load(target, reference)
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(target, Base, "offline.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("offline recovery is already running")
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	machine, err := os.ReadFile(filepath.Join(target, "etc/machine-id"))
	if err != nil || digest(machine) != p.Manifest.Machine || dest.UUID != p.Manifest.UUID {
		return errors.New("recovery identity does not match this filesystem")
	}
	// Remove only locks that Restic itself identifies as stale. Never force
	// removal of an active lock from another recovery/administrator process.
	if _, err = p.restic(nil, "unlock"); err != nil {
		return err
	}
	if _, err = p.restic(nil, "check", "--read-data"); err != nil {
		return err
	}
	marker := filepath.Join(target, Base, "recovery-required")
	if err = atomicFile(marker, []byte(reference+"\n"), 0600); err != nil {
		return err
	}
	if err = p.Mark("restoring"); err != nil {
		return err
	}
	if err = WriteRecord(filepath.Join(target, Base), Record{Kind: "restic-offline", Reference: reference, State: "restoring"}); err != nil {
		return err
	}
	// Restore each fixed subtree into its matching directory. Restic does not
	// allow combining --include and --exclude. Subtree selection also prevents
	// --delete from traversing home, service data or the recovery repository.
	for _, path := range roots {
		args := []string{"restore", p.Manifest.Snapshot + ":" + path, "--target", filepath.Join(target, path), "--delete", "--verify"}
		if path == "/usr" {
			args = append(args, "--exclude", "/lib/sysimage/rpm")
		}
		if path == "/boot" {
			args = append(args, "--exclude", "/efi")
		}
		if _, err = p.restic(nil, args...); err != nil {
			return err
		}
	}
	// Rebuild in a new private directory, then replace the offline database.
	database := filepath.Join(target, dbPath)
	temporary, err := os.MkdirTemp(filepath.Dir(database), ".nvidia-rpmdb-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	headers, err := os.Open(filepath.Join(p.Directory, "rpm-headers"))
	if err != nil {
		return err
	}
	defer headers.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := command(ctx, "rpmdb", "--dbpath", temporary, "--importdb")
	c.Stdin = headers
	if _, err = c.CombinedOutput(); err != nil {
		return fmt.Errorf("restore RPM database: %w", err)
	}
	if err = os.Chmod(temporary, 0755); err != nil {
		return err
	}
	// Atomic exchange keeps a valid directory at the RPMdb path even if power
	// fails here. Retrying recreates it from the verified export again.
	if err = unix.Renameat2(unix.AT_FDCWD, temporary, unix.AT_FDCWD, database, unix.RENAME_EXCHANGE); err != nil {
		return err
	}
	actual, err := inventory(target)
	if err != nil || !equalInventory(actual, p.Manifest.Inventory) {
		return errors.New("restored RPM inventory does not match the recovery point")
	}
	if err = p.Mark("restored"); err != nil {
		return err
	}
	if err = WriteRecord(filepath.Join(target, Base), Record{Kind: "restic-offline", Reference: reference, State: "restored"}); err != nil {
		return err
	}
	if err = os.Remove(marker); err != nil {
		return err
	}
	// Flush restored filesystem contents before the administrator reboots.
	syscall.Sync()
	return nil
}
