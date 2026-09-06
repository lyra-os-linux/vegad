package dbusserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// restoreBackup never lets restic write into an existing destination. Verification
// must succeed before any existing entry is moved. Both D-Bus restore methods use
// this path, including selected-item restores.
func restoreBackup(cfg BackupConfig, snapshotID, target, mode string, paths []string, report progressFunc) error {
	if mode != "replace" && mode != "separate-folder" {
		return fmt.Errorf("modo de restauração desconhecido: %s", mode)
	}
	if snapshotID == "" || strings.ContainsAny(snapshotID, "/\\\x00") || snapshotID == "." || snapshotID == ".." || strings.HasPrefix(snapshotID, "-") {
		return fmt.Errorf("identificador de snapshot inválido")
	}
	if report == nil {
		report = func(uint32, string) {}
	}
	return stagedRestore(target, mode, snapshotID, func(stage string) error {
		args := []string{"restore", snapshotID, "--target", stage, "--verify"}
		for _, path := range filterEmpty(paths) {
			args = append(args, "--include", path)
		}
		// Completion belongs to the entire operation, not just extraction.
		return runResticCommand(cfg, args, func(percent uint32, message string) {
			report(percent*9/10, message)
		}, "Restaurando em área temporária...", "Dados restaurados e verificados")
	}, func() { report(100, "Restauração concluída") })
}

type restoreMove struct {
	Path        string
	HadOriginal bool
	oldMoved    bool
	newMoved    bool
}

// Existing directories are merged without changing their metadata. Files and
// symlinks in the selection replace entries of the same (non-directory) kind;
// directory/type conflicts are rejected before applying anything. Unselected
// entries are never removed. rename preserves file metadata and hard links.
func planRestore(stage, target, relative string, moves *[]restoreMove) error {
	entries, err := os.ReadDir(filepath.Join(stage, relative))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		rel := filepath.Join(relative, entry.Name())
		if strings.HasPrefix(entry.Name(), ".vega-restore-") {
			return fmt.Errorf("nome reservado na restauração: %s", rel)
		}
		destination, err := os.Lstat(filepath.Join(target, rel))
		if errors.Is(err, fs.ErrNotExist) {
			*moves = append(*moves, restoreMove{Path: rel})
			continue
		}
		if err != nil {
			return err
		}
		if entry.IsDir() && destination.IsDir() {
			if err := planRestore(stage, target, rel, moves); err != nil {
				return err
			}
			continue
		}
		if entry.IsDir() || destination.IsDir() {
			return fmt.Errorf("conflito entre diretório e arquivo em %s; use uma pasta separada", rel)
		}
		*moves = append(*moves, restoreMove{Path: rel, HadOriginal: true})
	}
	return nil
}

func stagedRestore(target, mode, snapshotID string, extract func(string) error, finished func()) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	// Do not follow a replaced destination symlink, and serialize restores to
	// the same directory. A stable directory descriptor anchors all merge paths.
	dir, err := openRestoreDirectory(target)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := syscall.Flock(int(dir.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("destino já está em restauração: %w", err)
	}
	defer syscall.Flock(int(dir.Fd()), syscall.LOCK_UN)
	anchor := fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), dir.Fd())
	work, err := os.MkdirTemp(anchor, ".vega-restore-")
	if err != nil {
		return err
	}
	recoveryPath := filepath.Join(target, filepath.Base(work))
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(work)
		}
	}()
	stage, originals := filepath.Join(work, "new"), filepath.Join(work, "originals")
	for _, path := range []string{stage, originals} {
		if err := os.Mkdir(path, 0o700); err != nil {
			return err
		}
	}
	if err := extract(stage); err != nil {
		return err
	}
	if err := syncRestoreTree(stage); err != nil {
		return fmt.Errorf("sincronizar dados restaurados antes da substituição: %w", err)
	}
	var moves []restoreMove
	if mode == "separate-folder" {
		name := "restored-" + snapshotID
		if _, err := os.Lstat(filepath.Join(anchor, name)); !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("a pasta de restauração %s já existe ou está inacessível", name)
		}
		if err := os.Chmod(stage, 0o755); err != nil {
			return err
		}
		if err := os.Rename(stage, filepath.Join(anchor, name)); err != nil {
			return err
		}
	} else {
		if err := planRestore(stage, anchor, "", &moves); err != nil {
			return err
		}
		// Persist the recovery map before touching originals. If interrupted,
		// originals/ contains the pre-restore entries at their relative paths.
		journal, err := json.MarshalIndent(struct {
			Target string
			Moves  []restoreMove
		}{target, moves}, "", "  ")
		if err != nil {
			return err
		}
		if err := writeRestoreJournal(filepath.Join(work, "recovery.json"), journal); err != nil {
			return err
		}
		if err := dir.Sync(); err != nil {
			return err
		}
		if err := applyRestoreMoves(stage, anchor, originals, moves, os.Rename); err != nil {
			// Keep both versions even after successful automatic rollback. Never
			// delete the only remaining copy if compensation itself failed.
			keep = true
			return fmt.Errorf("%w; dados e mapa de recuperação preservados em %s", err, recoveryPath)
		}
	}
	if err := dir.Sync(); err != nil {
		keep = true
		return fmt.Errorf("sincronizar restauração: %w; recuperação em %s", err, recoveryPath)
	}
	finished()
	return nil
}

func syncRestoreTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Never open symlinks, devices or FIFOs from a snapshot as regular files.
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		return file.Sync()
	})
}

func syncRestoreOriginal(originals, relative string) error {
	path := filepath.Join(originals, relative)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		err = file.Sync()
		file.Close()
		if err != nil {
			return err
		}
	}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		dir, err := os.Open(parent)
		if err != nil {
			return err
		}
		err = dir.Sync()
		dir.Close()
		if err != nil || parent == originals {
			return err
		}
	}
}

func writeRestoreJournal(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Open each component without following symlinks, including during rollback.
// Go 1.22 is supported, so use Linux openat instead of the newer os.Root API.
func openRestoreDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("diretório de restauração deve ser absoluto")
	}
	current, err := os.Open("/")
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/") {
		if component == "" {
			continue
		}
		fd, err := syscall.Openat(int(current.Fd()), component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		current.Close()
		if err != nil {
			return nil, fmt.Errorf("abrir diretório %s: %w", path, err)
		}
		current = os.NewFile(uintptr(fd), component)
	}
	return current, nil
}

// The target anchor itself is a trusted procfs fd link; only relative parent
// components from the verified stage are traversed, with O_NOFOLLOW.
func restoreParent(target, relative string) (*os.File, string, error) {
	current, err := os.Open(target)
	if err != nil {
		return nil, "", err
	}
	parent := filepath.Dir(relative)
	if parent != "." {
		for _, component := range strings.Split(parent, string(filepath.Separator)) {
			fd, err := syscall.Openat(int(current.Fd()), component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
			current.Close()
			if err != nil {
				return nil, "", err
			}
			current = os.NewFile(uintptr(fd), component)
		}
	}
	return current, fmt.Sprintf("/proc/%d/fd/%d/%s", os.Getpid(), current.Fd(), filepath.Base(relative)), nil
}

func applyRestoreMoves(stage, target, originals string, moves []restoreMove, rename func(string, string) error) (result error) {
	defer func() {
		if result == nil {
			return
		}
		for i := len(moves) - 1; i >= 0; i-- {
			move := &moves[i]
			if !move.newMoved && !move.oldMoved {
				continue
			}
			parent, destination, err := restoreParent(target, move.Path)
			if err == nil {
				if move.newMoved {
					err = rename(destination, filepath.Join(stage, move.Path))
				}
				if err == nil && move.oldMoved {
					err = rename(filepath.Join(originals, move.Path), destination)
				}
				parent.Close()
			}
			if err != nil {
				result = errors.Join(result, fmt.Errorf("recuperar %s: %w", move.Path, err))
			}
		}
	}()
	for i := range moves {
		move := &moves[i]
		if err := os.MkdirAll(filepath.Dir(filepath.Join(originals, move.Path)), 0o700); err != nil {
			return err
		}
		parent, destination, err := restoreParent(target, move.Path)
		if err != nil {
			return err
		}
		err = func() error {
			defer parent.Close()
			if move.HadOriginal {
				info, err := os.Lstat(destination)
				if err != nil {
					return err
				}
				if info.IsDir() {
					return fmt.Errorf("destino virou diretório durante a restauração: %s", move.Path)
				}
				if err := rename(destination, filepath.Join(originals, move.Path)); err != nil {
					return err
				}
				move.oldMoved = true
				if err := syncRestoreOriginal(originals, move.Path); err != nil {
					return err
				}
				if err := parent.Sync(); err != nil {
					return err
				}
			} else if _, err := os.Lstat(destination); !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("destino mudou durante a restauração: %s", move.Path)
			}
			if err := rename(filepath.Join(stage, move.Path), destination); err != nil {
				return err
			}
			move.newMoved = true
			return parent.Sync()
		}()
		if err != nil {
			return err
		}
	}
	return nil
}
