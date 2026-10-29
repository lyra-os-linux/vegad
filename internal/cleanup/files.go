package cleanup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

const coredumpDir = "/var/lib/systemd/coredump"

// measureCoredumps counts the regular files directly in the coredump
// directory; systemd-coredump never creates subdirectories there.
func measureCoredumps(_ context.Context, env *Env) (measurement, error) {
	files, err := coredumpFiles(env)
	if err != nil {
		return measurement{}, err
	}
	m := measurement{count: len(files)}
	for _, file := range files {
		m.bytes += file.size
	}
	return m, nil
}

type coredumpFile struct {
	path string
	size uint64
}

func coredumpFiles(env *Env) ([]coredumpFile, error) {
	dir := env.path(coredumpDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []coredumpFile
	for _, entry := range entries {
		// Type comes from Lstat: a link is never followed nor counted.
		if !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, coredumpFile{filepath.Join(dir, entry.Name()), allocated(info)})
	}
	return files, nil
}

func cleanCoredumps(_ context.Context, env *Env, _ measurement) error {
	files, err := coredumpFiles(env)
	if err != nil {
		return err
	}
	var errs []error
	for _, file := range files {
		if err := os.Remove(file.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

const packageCacheDir = "/var/cache/zypp/packages"

func measurePackageCache(_ context.Context, env *Env) (measurement, error) {
	if reason := missingTool(env, "zypper", "zypper"); reason != "" {
		return measurement{reason: reason}, nil
	}
	size, files, err := treeSize(env.path(packageCacheDir), map[[2]uint64]bool{})
	return measurement{bytes: size, count: files}, err
}

// cleanPackageCache runs "zypper clean", which removes the package cache and
// keeps the metadata: what was measured is what goes away, and the next
// refresh does not download every repository again.
func cleanPackageCache(ctx context.Context, env *Env, _ measurement) error {
	_, err := runOK(ctx, env, "", "zypper", "-n", "clean")
	return err
}
