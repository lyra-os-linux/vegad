package distro

import (
	"bytes"
	"context"
	"os/exec"
	"syscall"
	"time"
)

// Preparation commands may finish their current metadata/RPM-key write after
// cancellation. No new command starts; a stuck process group is then terminated.
const preparationDrainTimeout = 30 * time.Second
const preparationTermTimeout = 5 * time.Second

func preparationCommandOutput(ctx context.Context, cmd *exec.Cmd) (string, error) {
	return drainCommand(ctx, cmd, preparationDrainTimeout, preparationTermTimeout)
}
func drainCommand(ctx context.Context, cmd *exec.Cmd, drain, term time.Duration) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = term
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if ctx.Err() != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			return output.String(), ctx.Err()
		}
		return output.String(), err
	case <-ctx.Done():
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // include surviving descendants
	timer := time.NewTimer(drain)
	defer timer.Stop()
	select {
	case <-done:
		return output.String(), ctx.Err()
	case <-timer.C:
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	timer.Reset(term)
	select {
	case <-done:
	case <-timer.C:
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
	return output.String(), ctx.Err()
}

func NewPreparationPackageBackend(ctx context.Context, id ID) (PackageBackend, error) {
	provider, err := NewProvider(id)
	if err != nil {
		return nil, err
	}
	backend := provider.Package().(*zypperBackend)
	backend.preparationContext = ctx
	return backend, nil
}
func (z *zypperBackend) preparationOutput(name string, args ...string) (string, error) {
	cmd := packageCommand(name, args...)
	cmd.Env = commandEnvC()
	if z.preparationContext != nil {
		return preparationCommandOutput(z.preparationContext, cmd)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}
func (z *zypperBackend) repoKeyOutput(args ...string) (string, error) {
	return z.preparationOutput("zypper", args...)
}
func (z *zypperBackend) readRepoKeyIdentity(repo string) (repoKeyIdentity, error) {
	out, err := z.repoKeyOutput("--xmlout", "--non-interactive", "repos", "--details")
	if err != nil {
		return repoKeyIdentity{}, err
	}
	return parseRepoKeyIdentity(out, repo)
}
