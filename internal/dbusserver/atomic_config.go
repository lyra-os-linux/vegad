package dbusserver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func writeConfigAtomically(path string, data []byte) error {
	return writeConfigAtomicallyWithMode(path, data, 0o644)
}

func writeConfigAtomicallyWithMode(path string, data []byte, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("configuração não é um arquivo regular: %s", path)
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".vega-config-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if info != nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			if err := file.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
				return err
			}
		}
		if err := copyConfigXattrs(path, file.Name()); err != nil {
			return err
		}
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func copyConfigXattrs(source, destination string) error {
	size, err := syscall.Listxattr(source, nil)
	if errors.Is(err, syscall.ENOTSUP) {
		return nil
	}
	if err != nil {
		return err
	}
	names := make([]byte, size)
	if _, err := syscall.Listxattr(source, names); err != nil {
		return err
	}
	for _, name := range strings.Split(string(names), "\x00") {
		if name == "" {
			continue
		}
		size, err := syscall.Getxattr(source, name, nil)
		if err != nil {
			return err
		}
		value := make([]byte, size)
		if _, err := syscall.Getxattr(source, name, value); err != nil {
			return err
		}
		if err := syscall.Setxattr(destination, name, value, 0); err != nil {
			return err
		}
	}
	return nil
}
