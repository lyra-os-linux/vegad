package dbusserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type backupMount struct {
	ID       int    `json:"id"`
	Target   string `json:"target"`
	Source   string `json:"source"`
	UUID     string `json:"uuid"`
	UniqueID uint64 `json:"unique_id,omitempty"`
}

func findBackupMount(selector, value string) (backupMount, error) {
	cmd := exec.Command("findmnt", "--json", "--list", "--output", "ID,TARGET,SOURCE,UUID", selector, value)
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return backupMount{}, errBackupDeferred
		}
		return backupMount{}, fmt.Errorf("findmnt: %w", err)
	}
	var result struct {
		Filesystems []backupMount `json:"filesystems"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return backupMount{}, err
	}
	for _, mount := range result.Filesystems {
		if mount.ID <= 0 || !filepath.IsAbs(mount.Target) {
			continue
		}
		// A mountpoint may have stacked mounts. Only accept the filesystem
		// actually visible at that path, rather than the first findmnt row.
		if file, err := openBackupMount(mount); err == nil {
			mount.UniqueID = backupMountUniqueID(file)
			file.Close()
			return mount, nil
		}
	}
	return backupMount{}, errBackupDeferred
}

func backupConnectionDir() string {
	return filepath.Join(backupStateDir(), "connections")
}

func backupMountTargetPath(id string) string {
	return filepath.Join(backupConnectionDir(), id+".target")
}

// Without a UUID, remember the mounted source and mountpoint during creation.
// Falling back to a parent filesystem after unplugging must never count as a
// new connection. The root filesystem cannot be an on-connect destination.
func prepareBackupMountTarget(cfg BackupConfig) error {
	if cfg.Frequency != "on-connect" || cfg.DestinationUUID != "" {
		return nil
	}
	if _, err := os.Stat(backupMountTargetPath(cfg.Id)); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// findmnt --target on Leap requires the queried path to exist. A new
	// repository may have multiple missing components: inspect its nearest
	// existing ancestor without creating directories on an absent volume.
	target := cfg.Destination
	for {
		if _, err := os.Lstat(target); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(target)
		if parent == target {
			return errBackupDeferred
		}
		target = parent
	}
	mount, err := findBackupMount("--target", target)
	if err != nil {
		return fmt.Errorf("monte o destino antes de configurar on-connect sem UUID: %w", err)
	}
	if filepath.Clean(mount.Target) == "/" {
		return fmt.Errorf("on-connect requer um volume montado separado ou o UUID do destino")
	}
	data, err := json.Marshal(mount)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(backupConnectionDir(), 0o700); err != nil {
		return err
	}
	return writeConfigAtomicallyWithMode(backupMountTargetPath(cfg.Id), data, 0o600)
}

func mountedBackupDestination(cfg BackupConfig) (backupMount, string, error) {
	if cfg.DestinationUUID != "" {
		mount, err := findBackupMount("--source", backupRemovableUUIDPath(cfg.DestinationUUID))
		relative := strings.TrimLeft(cfg.Destination, "/")
		if relative == "" {
			relative = "Vega"
		}
		if !filepath.IsLocal(relative) {
			return backupMount{}, "", fmt.Errorf("destino relativo inválido")
		}
		return mount, relative, err
	}
	data, err := os.ReadFile(backupMountTargetPath(cfg.Id))
	if err != nil {
		return backupMount{}, "", fmt.Errorf("destino on-connect sem identidade de montagem; recrie a configuração com o volume montado ou informe seu UUID: %w", err)
	}
	var expected backupMount
	if err := json.Unmarshal(data, &expected); err != nil {
		return backupMount{}, "", err
	}
	mount, err := findBackupMount("--mountpoint", expected.Target)
	if err != nil {
		return backupMount{}, "", err
	}
	if mount.Source != expected.Source || mount.UUID != expected.UUID {
		return backupMount{}, "", errBackupDeferred
	}
	relative, err := filepath.Rel(mount.Target, cfg.Destination)
	if err != nil || !filepath.IsLocal(relative) {
		return backupMount{}, "", fmt.Errorf("destino fora da montagem esperada")
	}
	return mount, relative, nil
}

// Anchor the child process to this mount, so an unmount cannot redirect writes
// into the host directory underneath it. Validate the mount ID after opening.
func openBackupMount(mount backupMount) (*os.File, error) {
	fd, err := syscall.Open(mount.Target, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), mount.Target)
	info, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", fd))
	if err == nil {
		for _, line := range strings.Split(string(info), "\n") {
			if strings.HasPrefix(line, "mnt_id:") && strings.TrimSpace(strings.TrimPrefix(line, "mnt_id:")) == strconv.Itoa(mount.ID) {
				if mount.UniqueID != 0 && backupMountUniqueID(file) != mount.UniqueID {
					file.Close()
					return nil, errBackupDeferred
				}
				return file, nil
			}
		}
	}
	file.Close()
	return nil, errBackupDeferred
}

func backupMountUniqueID(file *os.File) uint64 {
	var stat unix.Statx_t
	if err := unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID_UNIQUE, &stat); err != nil || stat.Mask&unix.STATX_MNT_ID_UNIQUE == 0 {
		return 0
	}
	return stat.Mnt_id
}

func withMountedBackup(cfg BackupConfig, run func(BackupConfig, backupMount) error) error {
	mount, relative, err := mountedBackupDestination(cfg)
	if err != nil {
		return err
	}
	anchor, err := openBackupMount(mount)
	if err != nil {
		return err
	}
	defer func() { anchor.Close() }()
	// Open/create each component relative to the mounted directory. No symlink
	// can redirect repository writes to another volume or the host filesystem.
	for _, component := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		if component == "." {
			continue
		}
		if err := syscall.Mkdirat(int(anchor.Fd()), component, 0o755); err != nil && !errors.Is(err, syscall.EEXIST) {
			return err
		}
		fd, err := syscall.Openat(int(anchor.Fd()), component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		anchor.Close()
		anchor = os.NewFile(uintptr(fd), component)
	}
	cfg.Destination = fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), anchor.Fd())
	cfg.DestinationUUID = ""
	cfg.Frequency = "manual"
	return run(cfg, mount)
}

func backupConnectionOnce(statePath, connection string, run func() error) error {
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(statePath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil
		}
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	data, err := os.ReadFile(statePath)
	if err == nil && string(data) == connection {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Record the attempt before running, including failure/interruption. A user
	// can explicitly retry with RunBackupNow; automatic retries wait for remount.
	if err := writeConfigAtomicallyWithMode(statePath, []byte(connection), 0o600); err != nil {
		return err
	}
	return run()
}

func runConnectedBackup(cfg BackupConfig, report progressFunc) error {
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return err
	}
	return withMountedBackup(cfg, func(resolved BackupConfig, mount backupMount) error {
		if mount.UniqueID == 0 {
			return fmt.Errorf("on-connect requer identificadores únicos de montagem (Linux 6.8 ou posterior); use a execução manual neste kernel")
		}
		connection := fmt.Sprintf("%s:%d:%s:%s", strings.TrimSpace(string(bootID)), mount.UniqueID, mount.Source, mount.Target)
		return backupConnectionOnce(filepath.Join(backupConnectionDir(), cfg.Id+".last"), connection, func() error {
			return WithShutdownInhibit("Backup: "+cfg.Id, func() error {
				return runBackupConfig(resolved, report)
			})
		})
	})
}

func RunScheduledBackupJob(ctx context.Context, configID string, report func(uint32, string)) error {
	cfg, err := readBackupConfig(configID)
	if err != nil {
		return err
	}
	if cfg.Frequency == "on-connect" {
		return RunBackupOnConnect(ctx, configID, report)
	}
	return WithShutdownInhibit("Backup: "+configID, func() error {
		return runBackupConfig(cfg, report)
	})
}

// Used by administrators to prepare an existing non-UUID on-connect config
// while its intended destination is mounted, without changing its credential.
func PrepareBackupMountTarget(configID string) error {
	backupConfigMu.Lock()
	defer backupConfigMu.Unlock()
	cfg, err := readBackupConfig(configID)
	if err != nil {
		return err
	}
	if cfg.Frequency != "on-connect" {
		return fmt.Errorf("configuração não usa on-connect")
	}
	if err := prepareBackupMountTarget(cfg); err != nil {
		return err
	}
	if err := writeBackupSystemdUnitsAt(cfg, backupSystemdDir); err != nil {
		return err
	}
	return activateBackupSystemdUnits(cfg)
}

// Keep the .path-triggered service running while its condition remains true.
// This works with existing oneshot units as well as the new simple units:
// returning after a backup would cause PathExists to trigger immediately again.
func RunBackupOnConnect(ctx context.Context, configID string, report func(uint32, string)) error {
	cfg, err := readBackupConfig(configID)
	if err != nil {
		return err
	}
	if cfg.Frequency != "on-connect" {
		return fmt.Errorf("configuração não usa on-connect")
	}
	return watchBackupConnection(ctx, 5*time.Second,
		func() bool { return backupDestinationPathAvailable(backupPathTrigger(cfg)) },
		func() error { return runConnectedBackup(cfg, report) },
		func(err error) {
			if report != nil {
				report(0, err.Error())
			}
		},
	)
}

func watchBackupConnection(ctx context.Context, interval time.Duration, present func() bool, attempt func() error, reportError func(error)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastError := ""
	for present() {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if err := attempt(); err != nil && !errors.Is(err, errBackupDeferred) {
			if err.Error() != lastError {
				reportError(err)
				lastError = err.Error()
			}
		} else {
			lastError = ""
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
	return nil
}
