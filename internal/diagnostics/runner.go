package diagnostics

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Runner executes a tool and returns its stdout and exit code. A non-zero
// exit is not an error: smartctl, rpm and btrfs encode results in it. err is
// set only when the tool could not run or ctx ended.
type Runner func(ctx context.Context, name string, args ...string) (stdout []byte, exitCode int, err error)

const outputLimit = 32 << 20

// limitedBuffer keeps at most outputLimit bytes. The buffer is a named
// field, not embedded: an embedded bytes.Buffer brings its ReadFrom, and
// io.Copy (used by os/exec) would call it and skip this Write and its limit.
type limitedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := outputLimit - b.buf.Len(); room < len(p) {
		if room > 0 {
			b.buf.Write(p[:room])
		}
		b.truncated = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte { return b.buf.Bytes() }

// ExecRunner runs tools in their own process group with the C locale, so a
// cancel kills every child (stress-ng workers, fio jobs) and parsers always
// see untranslated output.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	out, code, err, _ := execRun(ctx, name, args...)
	return out, code, err
}

// execRun is ExecRunner that also says whether the output passed
// outputLimit and was cut.
func execRun(ctx context.Context, name string, args ...string) ([]byte, int, error, bool) {
	cmd := exec.Command(name, args...)
	cmd.Env = []string{"LC_ALL=C", "LANG=C", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout limitedBuffer
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		return nil, -1, err, false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return stdout.Bytes(), exitCode(err), waitError(err), stdout.truncated
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return stdout.Bytes(), -1, ctx.Err(), stdout.truncated
	}
}

// truncatedTools records the tools whose output was cut during a stage.
type truncatedTools struct {
	mu    sync.Mutex
	tools []string
}

func (e *Env) noteTruncated(tool string) {
	if e.truncated == nil {
		return
	}
	e.truncated.mu.Lock()
	defer e.truncated.mu.Unlock()
	for _, known := range e.truncated.tools {
		if known == tool {
			return
		}
	}
	e.truncated.tools = append(e.truncated.tools, tool)
}

// TakeTruncated returns and clears the tools whose output passed
// outputLimit since the last call. The service calls it around every stage.
func (e *Env) TakeTruncated() []string {
	if e.truncated == nil {
		return nil
	}
	e.truncated.mu.Lock()
	defer e.truncated.mu.Unlock()
	tools := e.truncated.tools
	e.truncated.tools = nil
	return tools
}

// TruncationFinding warns that a stage read only the first outputLimit
// bytes of some tools, so its result may be incomplete.
func (e *Env) TruncationFinding(tools []string) Finding {
	list := strings.Join(tools, ", ")
	return e.finding(Info, "stage.output-truncated", list, "tools", list)
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

func waitError(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil
	}
	return err
}

// WorkDir is where disk-bench writes its file. vegad removes it after every
// run and again at startup, in case a crash left a benchmark file behind.
const WorkDir = "/var/tmp/vega-diagnostics"

// HostEnv is the real system, with the contract's default options.
func HostEnv(locale string, bootsBack int) *Env {
	env := &Env{
		Locale:          locale,
		BootsBack:       bootsBack,
		DurationSeconds: 240,
		MemoryPercent:   70,
		Run:             ExecRunner,
		ReadFile:        os.ReadFile,
		Glob:            filepath.Glob,
		LookPath:        exec.LookPath,
		Readlink:        os.Readlink,
		Profile:         "desktop",
		Statfs:          statfs,
		NumCPU:          runtime.NumCPU,
		WorkDir:         WorkDir,
		SampleInterval:  5 * time.Second,
		truncated:       &truncatedTools{},
	}
	env.Run = func(ctx context.Context, name string, args ...string) ([]byte, int, error) {
		out, code, err, cut := execRun(ctx, name, args...)
		if cut {
			env.noteTruncated(name)
		}
		return out, code, err
	}
	return env
}

func statfs(path string) (uint64, uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	return st.Blocks * uint64(st.Bsize), st.Bavail * uint64(st.Bsize), nil
}

// CleanupWorkDir removes benchmark leftovers. Safe to call when absent.
func CleanupWorkDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
