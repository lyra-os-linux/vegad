package dbusserver

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	backupStateDirDefault = "/etc/vega/backup"
	backupRepoPassSuffix   = ".password"
)

var (
	errBackupUnavailable = errors.New("restic não está disponível neste sistema")
	backupIDRe           = regexp.MustCompile(`[^a-z0-9]+`)
)

// BackupService backs org.lyraos.Vega1.Backup (PROMPT-VEGA-MODULO-BACKUP.md
// §4): orchestrates restic subprocesses per backup configuration. Configs
// live under /etc/vega/backup by default; the path can be overridden in
// tests/dev with VEGA_BACKUP_STATE_DIR.
type BackupService struct {
	activity *Activity
	conn     *dbus.Conn
	nextTxID atomic.Uint32
}

type BackupConfig struct {
	Id          string
	Paths       []string
	Destination string
	Frequency   string // "daily", "weekly", "on-connect", "manual"
}

type BackupSnapshotInfo struct {
	Id        string
	Timestamp int64
	FileCount uint64
	SizeBytes uint64
}

type resticSnapshot struct {
	ID       string `json:"id"`
	Time     string `json:"time"`
	ShortID  string `json:"short_id"`
	Paths    []string `json:"paths"`
	Summary  struct {
		FilesProcessed uint64 `json:"total_files_processed"`
		BytesProcessed uint64 `json:"total_bytes_processed"`
	} `json:"summary"`
	Parent string `json:"parent"`
}

func backupStateDir() string {
	if v := strings.TrimSpace(os.Getenv("VEGA_BACKUP_STATE_DIR")); v != "" {
		return v
	}
	return backupStateDirDefault
}

func backupConfigsDir() string {
	return filepath.Join(backupStateDir(), "configs")
}

func backupPasswordsDir() string {
	return filepath.Join(backupStateDir(), "passwords")
}

func backupConfigPath(id string) string {
	return filepath.Join(backupConfigsDir(), id+".json")
}

func backupPasswordPath(id string) string {
	return filepath.Join(backupPasswordsDir(), id+backupRepoPassSuffix)
}

func resticAvailable() bool {
	_, err := exec.LookPath("restic")
	return err == nil
}

func (b *BackupService) CreateConfig(cfg BackupConfig) (string, *dbus.Error) {
	b.activity.Touch()
	normalized, err := normalizeBackupConfig(cfg)
	if err != nil {
		return "", dbus.MakeFailedError(err)
	}
	if err := ensureBackupDirs(); err != nil {
		return "", dbus.MakeFailedError(err)
	}

	cfgPath := backupConfigPath(normalized.Id)
	if _, err := os.Stat(cfgPath); err == nil {
		return "", dbus.MakeFailedError(fmt.Errorf("configuração %q já existe", normalized.Id))
	}

	if err := writeBackupConfig(cfgPath, normalized); err != nil {
		return "", dbus.MakeFailedError(err)
	}
	if err := ensureBackupPassword(normalized.Id); err != nil {
		return "", dbus.MakeFailedError(err)
	}

	if err := ensureResticRepository(normalized, nil); err != nil && !errors.Is(err, errBackupUnavailable) {
		return "", dbus.MakeFailedError(err)
	}
	return normalized.Id, nil
}

func (b *BackupService) ListConfigs() ([]BackupConfig, *dbus.Error) {
	b.activity.Touch()
	rows, err := readBackupConfigs()
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	return rows, nil
}

func (b *BackupService) RunBackupNow(configID string) (uint32, *dbus.Error) {
	b.activity.Touch()
	cfg, err := readBackupConfig(configID)
	if err != nil {
		return 0, dbus.MakeFailedError(err)
	}
	return b.startTransaction(b.emitBackupProgress, b.emitBackupFinished, func(report progressFunc) error {
		if err := ensureResticRepository(cfg, report); err != nil {
			return err
		}
		args := []string{"backup"}
		args = append(args, cfg.Paths...)
		return runResticCommand(cfg, args, report, "Iniciando backup...", "Backup concluído")
	}), nil
}

func (b *BackupService) ListSnapshots(configID string) ([]BackupSnapshotInfo, *dbus.Error) {
	b.activity.Touch()
	cfg, err := readBackupConfig(configID)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	if err := ensureResticRepository(cfg, nil); err != nil {
		return nil, dbus.MakeFailedError(err)
	}

	snaps, err := resticSnapshots(cfg)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	return snaps, nil
}

// mode: "replace" or "separate-folder" (see spec §3.5.4).
func (b *BackupService) RestoreSnapshot(snapshotID, targetPath, mode string) (uint32, *dbus.Error) {
	b.activity.Touch()
	cfg, err := findBackupConfigBySnapshot(snapshotID)
	if err != nil {
		return 0, dbus.MakeFailedError(err)
	}

	return b.startTransaction(b.emitRestoreProgress, b.emitRestoreFinished, func(report progressFunc) error {
		if err := ensureResticRepository(cfg, report); err != nil {
			return err
		}

		restoreTarget := targetPath
		switch mode {
		case "replace":
			if err := os.RemoveAll(targetPath); err != nil {
				return err
			}
		case "separate-folder":
			restoreTarget = filepath.Join(targetPath, "restored-"+snapshotID)
		default:
			return fmt.Errorf("modo de restauração desconhecido: %s", mode)
		}

		if err := os.MkdirAll(restoreTarget, 0o755); err != nil {
			return err
		}
		return runResticCommand(cfg, []string{"restore", snapshotID, "--target", restoreTarget}, report, "Iniciando restauração...", "Restauração concluída")
	}), nil
}

func (b *BackupService) DeleteConfig(configID string) *dbus.Error {
	b.activity.Touch()
	if err := os.Remove(backupConfigPath(configID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return dbus.MakeFailedError(err)
	}
	if err := os.Remove(backupPasswordPath(configID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (b *BackupService) emitBackupProgress(txID uint32, percent uint32, message string) error {
	return b.conn.Emit(ObjectPath, BusName+".Backup.BackupProgress", txID, percent, message)
}

func (b *BackupService) emitBackupFinished(txID uint32, success bool, message string) error {
	return b.conn.Emit(ObjectPath, BusName+".Backup.BackupFinished", txID, success, message)
}

func (b *BackupService) emitRestoreProgress(txID uint32, percent uint32, message string) error {
	return b.conn.Emit(ObjectPath, BusName+".Backup.RestoreProgress", txID, percent, message)
}

func (b *BackupService) emitRestoreFinished(txID uint32, success bool, message string) error {
	return b.conn.Emit(ObjectPath, BusName+".Backup.RestoreFinished", txID, success, message)
}

func (b *BackupService) startTransaction(
	emitProgress func(uint32, uint32, string) error,
	emitFinished func(uint32, bool, string) error,
	work func(report progressFunc) error,
) uint32 {
	txID := b.nextTxID.Add(1)
	report := func(percent uint32, message string) {
		if err := emitProgress(txID, percent, message); err != nil {
			fmt.Printf("vegad: emit transaction progress: %v\n", err)
		}
	}
	go func() {
		err := work(report)
		success := err == nil
		message := "Concluído"
		if err != nil {
			message = err.Error()
		}
		if emitErr := emitFinished(txID, success, message); emitErr != nil {
			fmt.Printf("vegad: emit transaction finished: %v\n", emitErr)
		}
	}()
	return txID
}

func normalizeBackupConfig(cfg BackupConfig) (BackupConfig, error) {
	cfg.Id = strings.TrimSpace(cfg.Id)
	if cfg.Id == "" {
		cfg.Id = slugifyBackupConfig(cfg.Destination)
	}
	cfg.Id = slugifyBackupConfig(cfg.Id)
	if cfg.Id == "" {
		cfg.Id = fmt.Sprintf("backup-%d", time.Now().Unix())
	}
	for i := range cfg.Paths {
		cfg.Paths[i] = strings.TrimSpace(cfg.Paths[i])
	}
	cfg.Paths = filterEmpty(cfg.Paths)
	cfg.Destination = strings.TrimSpace(cfg.Destination)
	cfg.Frequency = strings.TrimSpace(cfg.Frequency)
	if len(cfg.Paths) == 0 {
		return BackupConfig{}, fmt.Errorf("selecione ao menos um caminho para backup")
	}
	if cfg.Destination == "" {
		return BackupConfig{}, fmt.Errorf("selecione um destino para o backup")
	}
	switch cfg.Frequency {
	case "daily", "weekly", "on-connect", "manual":
	default:
		return BackupConfig{}, fmt.Errorf("frequência inválida: %s", cfg.Frequency)
	}
	return cfg, nil
}

func slugifyBackupConfig(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = backupIDRe.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	return value
}

func filterEmpty(values []string) []string {
	var out []string
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func ensureBackupDirs() error {
	for _, dir := range []string{backupStateDir(), backupConfigsDir(), backupPasswordsDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func writeBackupConfig(path string, cfg BackupConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func readBackupConfig(id string) (BackupConfig, error) {
	data, err := os.ReadFile(backupConfigPath(id))
	if err != nil {
		return BackupConfig{}, err
	}
	var cfg BackupConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return BackupConfig{}, err
	}
	return cfg, nil
}

func readBackupConfigs() ([]BackupConfig, error) {
	entries, err := os.ReadDir(backupConfigsDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var rows []BackupConfig
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		cfg, err := readBackupConfig(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			continue
		}
		rows = append(rows, cfg)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Id < rows[j].Id })
	return rows, nil
}

func findBackupConfigBySnapshot(snapshotID string) (BackupConfig, error) {
	rows, err := readBackupConfigs()
	if err != nil {
		return BackupConfig{}, err
	}
	for _, cfg := range rows {
		snaps, err := resticSnapshots(cfg)
		if err != nil {
			continue
		}
		for _, snap := range snaps {
			if snap.Id == snapshotID {
				return cfg, nil
			}
		}
	}
	return BackupConfig{}, fmt.Errorf("snapshot %q não encontrado em nenhuma configuração", snapshotID)
}

func ensureBackupPassword(id string) error {
	path := backupPasswordPath(id)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	password := fmt.Sprintf("%x", buf)
	return os.WriteFile(path, []byte(password), 0o600)
}

func resticPasswordFile(id string) string {
	return backupPasswordPath(id)
}

func ensureResticRepository(cfg BackupConfig, report progressFunc) error {
	if !resticAvailable() {
		return errBackupUnavailable
	}
	if err := os.MkdirAll(cfg.Destination, 0o755); err != nil {
		return err
	}
	cmd := exec.Command("restic", "-r", cfg.Destination, "snapshots", "--json")
	cmd.Env = append(os.Environ(), "RESTIC_PASSWORD_FILE="+resticPasswordFile(cfg.Id))
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "is missing") || strings.Contains(string(out), "repository") {
			initCmd := exec.Command("restic", "-r", cfg.Destination, "init")
			initCmd.Env = append(os.Environ(), "RESTIC_PASSWORD_FILE="+resticPasswordFile(cfg.Id))
			if report != nil {
				report(5, "Inicializando repositório...")
			}
			if out, initErr := initCmd.CombinedOutput(); initErr != nil {
				return fmt.Errorf("restic init: %w — %s", initErr, strings.TrimSpace(string(out)))
			}
			return nil
		}
		return fmt.Errorf("restic snapshots: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func resticSnapshots(cfg BackupConfig) ([]BackupSnapshotInfo, error) {
	cmd := exec.Command("restic", "-r", cfg.Destination, "snapshots", "--json")
	cmd.Env = append(os.Environ(), "RESTIC_PASSWORD_FILE="+resticPasswordFile(cfg.Id))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("restic snapshots: %w — %s", err, strings.TrimSpace(string(out)))
	}

	var rows []resticSnapshot
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	outRows := make([]BackupSnapshotInfo, 0, len(rows))
	for _, row := range rows {
		ts, _ := time.Parse(time.RFC3339, row.Time)
		outRows = append(outRows, BackupSnapshotInfo{
			Id:        firstNonEmpty(row.ShortID, row.ID),
			Timestamp: ts.Unix(),
			FileCount: row.Summary.FilesProcessed,
			SizeBytes: row.Summary.BytesProcessed,
		})
	}
	return outRows, nil
}

func runResticCommand(cfg BackupConfig, args []string, report progressFunc, startMsg, doneMsg string) error {
	report(0, startMsg)

	cmd := exec.Command("restic", append([]string{"-r", cfg.Destination}, args...)...)
	cmd.Env = append(os.Environ(), "RESTIC_PASSWORD_FILE="+resticPasswordFile(cfg.Id))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return err
	}

	scanner := newLineScanner(stdout)
	var lastLines []string
	percent := uint32(10)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lastLines = append(lastLines, line)
		if len(lastLines) > 20 {
			lastLines = lastLines[1:]
		}
		if percent < 90 {
			percent += 5
		}
		report(percent, line)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("restic: %w — %s", err, strings.Join(lastLines, " | "))
	}
	report(100, doneMsg)
	return nil
}

func newLineScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Split(bufio.ScanLines)
	return scanner
}
