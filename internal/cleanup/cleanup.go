// Package cleanup measures and frees the space the system can give back
// without touching personal files (org.lyraos.Vega1.Cleanup,
// space-cleanup-v1). Every category is measured first; cleaning removes only
// what the measurement found, and nothing outside the category's paths.
package cleanup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Runner executes a tool with stdin and returns its stdout and exit code. A
// non-zero exit is not an error; err is set only when the tool could not run
// or ctx ended.
type Runner func(ctx context.Context, stdin, name string, args ...string) (stdout []byte, exitCode int, err error)

// Env is the host seen by the categories; tests replace every field.
type Env struct {
	// Root prefixes every path ("" on the host).
	Root     string
	Run      Runner
	LookPath func(string) (string, error)
	// Snapshot wraps a change in a pre/post snapshot pair.
	Snapshot func(action string, work func() error) error
	Now      func() time.Time
}

// HostEnv is the real system.
func HostEnv(snapshot func(string, func() error) error) *Env {
	return &Env{Run: ExecRunner, LookPath: exec.LookPath, Snapshot: snapshot, Now: time.Now}
}

func (e *Env) path(p string) string { return filepath.Join(e.Root, p) }

// Category is one row of Scan.
type Category struct {
	ID       string
	Bytes    uint64
	CanClean bool
	Detail   string
}

// measurement is what a category found; Detail is derived from it.
type measurement struct {
	bytes       uint64
	count       int
	items       []string
	sizeUnknown bool
	minimum     bool
	// reason blocks cleaning (busy, missing-tool:<pkg>, not-configured,
	// protected); empty when the category can be cleaned or is just empty.
	reason string
}

func (m measurement) cleanable() bool {
	return m.reason == "" && (m.bytes > 0 || m.count > 0)
}

const maxItems = 20

func (m measurement) category(id string) Category {
	var tokens []string
	if m.count > 0 || m.reason == "" {
		tokens = append(tokens, "count="+strconv.Itoa(m.count))
	}
	if m.sizeUnknown {
		tokens = append(tokens, "size=unknown")
	}
	if m.minimum {
		tokens = append(tokens, "minimum")
	}
	reason := m.reason
	if reason == "" && !m.cleanable() {
		reason = "nothing"
	}
	if reason != "" {
		tokens = append(tokens, "reason="+reason)
	}
	for i, item := range m.items {
		if i == maxItems {
			break
		}
		// ";" separates tokens; items never legitimately contain it.
		tokens = append(tokens, "item="+strings.ReplaceAll(item, ";", ","))
	}
	return Category{ID: id, Bytes: m.bytes, CanClean: m.cleanable(), Detail: strings.Join(tokens, ";")}
}

type category struct {
	id      string
	measure func(context.Context, *Env) (measurement, error)
	clean   func(context.Context, *Env, measurement) error
}

// IDs lists the categories in the order the frontend shows them.
var IDs = []string{"coredumps", "journal", "package-cache", "flatpak-unused", "old-kernels", "snapshots"}

var categories = map[string]category{
	"coredumps":      {"coredumps", measureCoredumps, cleanCoredumps},
	"journal":        {"journal", measureJournal, cleanJournal},
	"package-cache":  {"package-cache", measurePackageCache, cleanPackageCache},
	"flatpak-unused": {"flatpak-unused", measureFlatpak, cleanFlatpak},
	"old-kernels":    {"old-kernels", measureKernels, cleanKernels},
	"snapshots":      {"snapshots", measureSnapshots, cleanSnapshots},
}

// Known reports whether id is a system category.
func Known(id string) bool {
	_, ok := categories[id]
	return ok
}

// Scan measures every category without deleting anything. A category whose
// measurement fails is reported as not cleanable instead of failing the scan.
func Scan(ctx context.Context, env *Env) []Category {
	rows := make([]Category, 0, len(IDs))
	for _, id := range IDs {
		m, err := categories[id].measure(ctx, env)
		if err != nil && m.reason == "" {
			m = measurement{reason: "failed"}
		}
		rows = append(rows, m.category(id))
	}
	return rows
}

// Result of Clean for one category.
const (
	ResultOK      = "ok"
	ResultFailed  = "failed"
	ResultSkipped = "skipped"
)

// Clean measures id again, cleans exactly what it found and returns the bytes
// freed (before minus after). A category with nothing to clean is skipped.
func Clean(ctx context.Context, env *Env, id string) (freed uint64, result string, err error) {
	c, ok := categories[id]
	if !ok {
		return 0, ResultFailed, fmt.Errorf("categoria desconhecida: %s", id)
	}
	before, err := c.measure(ctx, env)
	if err != nil {
		return 0, ResultFailed, err
	}
	if !before.cleanable() {
		if before.reason != "" {
			return 0, ResultSkipped, errors.New(before.reason)
		}
		return 0, ResultSkipped, nil
	}
	if err := c.clean(ctx, env, before); err != nil {
		return 0, ResultFailed, err
	}
	after, err := c.measure(ctx, env)
	if err == nil && after.bytes < before.bytes {
		freed = before.bytes - after.bytes
	}
	return freed, ResultOK, nil
}

// allocated is the space a file takes on disk, the space removing it frees.
func allocated(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Blocks) * 512
	}
	return uint64(info.Size())
}

// treeSize sums the allocated size of every file under root without following
// symbolic links, without crossing into other file systems and counting hard
// links once. A missing root is empty.
func treeSize(root string, seen map[[2]uint64]bool) (uint64, int, error) {
	rootInfo, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	if !rootInfo.IsDir() {
		return 0, 0, nil
	}
	var dev uint64
	if st, ok := rootInfo.Sys().(*syscall.Stat_t); ok {
		dev = uint64(st.Dev)
	}
	var total uint64
	files := 0
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if ok && uint64(st.Dev) != dev {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if ok && st.Nlink > 1 {
			key := [2]uint64{uint64(st.Dev), uint64(st.Ino)}
			if seen[key] {
				return nil
			}
			seen[key] = true
		}
		total += allocated(info)
		files++
		return nil
	})
	return total, files, err
}

func missingTool(env *Env, tool, pkg string) string {
	if _, err := env.LookPath(tool); err != nil {
		return "missing-tool:" + pkg
	}
	return ""
}

// ExecRunner runs tools in their own process group with the C locale and the
// given stdin, so parsers see untranslated output and a cancel kills every
// child.
func ExecRunner(ctx context.Context, stdin, name string, args ...string) ([]byte, int, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = []string{"LC_ALL=C", "LANG=C", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = strings.NewReader(stdin)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		return nil, -1, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.Bytes(), exitErr.ExitCode(), nil
		}
		if err != nil {
			return stdout.Bytes(), -1, err
		}
		return stdout.Bytes(), 0, nil
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return stdout.Bytes(), -1, ctx.Err()
	}
}

// runOK runs a tool that must succeed.
func runOK(ctx context.Context, env *Env, stdin, name string, args ...string) ([]byte, error) {
	out, code, err := env.Run(ctx, stdin, name, args...)
	if err != nil {
		return out, err
	}
	if code != 0 {
		return out, fmt.Errorf("%s %s: código %d", name, strings.Join(args, " "), code)
	}
	return out, nil
}
