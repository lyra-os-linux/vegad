package dbusserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

func backupPendingPath(id string) string {
	return filepath.Join(backupConfigsDir(), id+".pending")
}

// A durable pending record reserves this ID and permits the exact same request
// to resume after any error or daemon restart. Never remove a prepared repo or
// its credential on failure: it may already contain data from an earlier run.
// afterStep is a fault-injection boundary; production passes nil.
func createBackupConfig(cfg BackupConfig, unitDir string, afterStep func(string) error) error {
	backupConfigMu.Lock()
	defer backupConfigMu.Unlock()
	if err := ensureBackupDirs(); err != nil {
		return err
	}
	pending := backupPendingPath(cfg.Id)
	data, err := os.ReadFile(pending)
	if err == nil {
		var saved BackupConfig
		if err := json.Unmarshal(data, &saved); err != nil {
			return fmt.Errorf("ler criação pendente: %w", err)
		}
		if !reflect.DeepEqual(cfg, saved) {
			return fmt.Errorf("configuração %q tem criação pendente; repita os mesmos parâmetros", cfg.Id)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		paths := []string{backupConfigPath(cfg.Id)}
		if cfg.Frequency != "manual" {
			for _, name := range []string{backupServiceUnitName(cfg.Id), backupTimerUnitName(cfg.Id), backupPathUnitName(cfg.Id)} {
				paths = append(paths, filepath.Join(unitDir, name))
			}
		}
		for _, path := range paths {
			if _, err := os.Lstat(path); err == nil {
				return fmt.Errorf("configuração %q já existe: %s", cfg.Id, path)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := writeBackupConfig(pending, cfg); err != nil {
			return err
		}
	} else {
		return err
	}

	steps := []struct {
		name string
		run  func() error
	}{
		{"pending", func() error { return nil }},
		{"password", func() error { return ensureBackupPassword(cfg.Id) }},
		{"repository", func() error {
			if cfg.Frequency == "on-connect" && !backupDestinationIsAvailable(cfg) {
				return nil
			}
			err := ensureResticRepository(cfg, nil)
			if errors.Is(err, errBackupDeferred) {
				return nil
			}
			return err
		}},
		{"units", func() error { return writeBackupSystemdUnitsAt(cfg, unitDir) }},
		{"config", func() error { return writeBackupConfig(backupConfigPath(cfg.Id), cfg) }},
		{"activation", func() error { return activateBackupSystemdUnits(cfg) }},
	}
	for _, step := range steps {
		err := step.run()
		if err == nil && afterStep != nil {
			err = afterStep(step.name)
		}
		if err != nil {
			return fmt.Errorf("criação %q pendente (%s); repita a criação para retomar: %w", cfg.Id, step.name, err)
		}
	}
	if err := os.Remove(pending); err != nil {
		return err
	}
	dir, err := os.Open(backupConfigsDir())
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
