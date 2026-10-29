package diagnostics

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Runner executes a tool and returns its stdout and exit code. A non-zero
// exit is not an error: smartctl, rpm and btrfs encode results in it. err is
// set only when the tool could not run or ctx ended.
type Runner func(ctx context.Context, name string, args ...string) (stdout []byte, exitCode int, err error)

const outputLimit = 32 << 20

type limitedBuffer struct {
	bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := outputLimit - b.Len(); room < len(p) {
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		b.truncated = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// ExecRunner runs tools in their own process group with the C locale, so a
// cancel kills every child (stress-ng workers, fio jobs) and parsers always
// see untranslated output.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = []string{"LC_ALL=C", "LANG=C", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout limitedBuffer
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		return nil, -1, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return stdout.Bytes(), exitCode(err), waitError(err)
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

// HostEnv is the real system.
func HostEnv(locale string, bootsBack int) *Env {
	return &Env{
		Locale:    locale,
		BootsBack: bootsBack,
		Run:       ExecRunner,
		ReadFile:  os.ReadFile,
		Glob:      filepath.Glob,
		LookPath:  exec.LookPath,
	}
}
