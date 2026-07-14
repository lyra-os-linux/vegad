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
	backupRepoPassSuffix  = ".password"
	backupSystemdDir      = "/etc/systemd/system"
)

var (
	errBackupUnavailable = errors.New("restic não está disponível neste sistema")
	errBackupDeferred    = errors.New("destino do backup indisponível; execução adiada")
	backupIDRe           = regexp.MustCompile(`[^a-z0-9]+`)
)

// BackupService backs org.lyraos.Vega1.Backup: orchestrates restic
// subprocesses per backup configuration. Configs
// live under /etc/vega/backup by default; the path can be overridden in
// tests/dev with VEGA_BACKUP_STATE_DIR.
type BackupService struct {
	activity *Activity
	conn     *dbus.Conn
	nextTxID atomic.Uint32
}

type BackupConfig struct {
	Id              string
	Paths           []string
	Destination     string
	DestinationUUID string
	Frequency       string // "daily", "weekly", "on-connect", "manual"
}

type BackupSnapshotInfo struct {
	Id        string
	Timestamp int64
	FileCount uint64
	SizeBytes uint64
}

type resticSnapshot struct {
	ID      string   `json:"id"`
	Time    string   `json:"time"`
	ShortID string   `json:"short_id"`
	Paths   []string `json:"paths"`
	Summary struct {
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

func backupFailuresDir() string {
	return filepath.Join(backupStateDir(), "failures")
}

func backupConfigPath(id string) string {
	return filepath.Join(backupConfigsDir(), id+".json")
}

func backupPasswordPath(id string) string {
	return filepath.Join(backupPasswordsDir(), id+backupRepoPassSuffix)
}

func backupFailureCountPath(id string) string {
	return filepath.Join(backupFailuresDir(), id+".json")
}

func backupDestinationPathAvailable(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func backupRemovableUUIDPath(uuid string) string {
	return filepath.Join("/dev/disk/by-uuid", strings.TrimSpace(uuid))
}

func backupDestinationMountPoint(uuid string) (string, error) {
	devPath := backupRemovableUUIDPath(uuid)
	if !backupDestinationPathAvailable(devPath) {
		return "", errBackupDeferred
	}
	if !commandAvailable("findmnt") {
		return "", fmt.Errorf("findmnt não está disponível")
	}
	out, err := runCommandOutput("findmnt", "--noheadings", "--output", "TARGET", "--source", devPath)
	if err != nil {
		return "", fmt.Errorf("findmnt %s: %w — %s", devPath, err, out)
	}
	mountPoint := strings.TrimSpace(out)
	if mountPoint == "" {
		return "", errBackupDeferred
	}
	return mountPoint, nil
}

func backupDestinationResolved(cfg BackupConfig) (string, error) {
	if strings.TrimSpace(cfg.DestinationUUID) == "" {
		return cfg.Destination, nil
	}
	mountPoint, err := backupDestinationMountPoint(cfg.DestinationUUID)
	if err != nil {
		return "", err
	}
	relative := strings.TrimLeft(cfg.Destination, "/")
	if relative == "" {
		relative = "Vega"
	}
	return filepath.Join(mountPoint, relative), nil
}

func backupPathTrigger(cfg BackupConfig) string {
	if strings.TrimSpace(cfg.DestinationUUID) != "" {
		return backupRemovableUUIDPath(cfg.DestinationUUID)
	}
	return cfg.Destination
}

func backupDestinationIsAvailable(cfg BackupConfig) bool {
	if strings.TrimSpace(cfg.DestinationUUID) != "" {
		_, err := backupDestinationMountPoint(cfg.DestinationUUID)
		return err == nil
	}
	return backupDestinationPathAvailable(cfg.Destination)
}

func resticAvailable() bool {
	_, err := exec.LookPath("restic")
	return err == nil
}

func secretToolAvailable() bool {
	_, err := exec.LookPath("secret-tool")
	return err == nil
}

func (b *BackupService) CreateConfig(sender dbus.Sender, cfg BackupConfig) (string, *dbus.Error) {
	b.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.backup.configure"); err != nil {
		return "", err
	}
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

	if normalized.Frequency != "on-connect" || backupDestinationIsAvailable(normalized) {
		if err := ensureResticRepository(normalized, nil); err != nil && !errors.Is(err, errBackupUnavailable) {
			return "", dbus.MakeFailedError(err)
		}
	}
	if err := writeBackupSystemdUnits(normalized); err != nil {
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

func (b *BackupService) RunBackupNow(sender dbus.Sender, configID string) (uint32, *dbus.Error) {
	b.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.backup.run"); err != nil {
		return 0, err
	}
	return b.startTransaction("Backup: "+configID, b.emitBackupProgress, b.emitBackupFinished, func(report progressFunc) error {
		err := RunBackupJob(configID, report)
		if err == nil {
			resetBackupFailureCount(configID)
			return nil
		}
		count, countErr := incrementBackupFailureCount(configID)
		if countErr != nil {
			return fmt.Errorf("%w; e falha ao registrar estado: %v", err, countErr)
		}
		if count >= 3 {
			if alertErr := b.emitBackupAlert(configID, count, err.Error()); alertErr != nil {
				fmt.Printf("vegad: emit backup alert: %v\n", alertErr)
			}
		}
		return err
	}), nil
}

func (b *BackupService) ListSnapshots(configID string) ([]BackupSnapshotInfo, *dbus.Error) {
	b.activity.Touch()
	cfg, err := readBackupConfig(configID)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	if err := ensureResticRepository(cfg, nil); err != nil {
		if errors.Is(err, errBackupDeferred) {
			return nil, nil
		}
		return nil, dbus.MakeFailedError(err)
	}

	snaps, err := resticSnapshots(cfg)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	return snaps, nil
}

// mode: "replace" or "separate-folder" (see spec §3.5.4).
func (b *BackupService) RestoreSnapshot(sender dbus.Sender, snapshotID, targetPath, mode string) (uint32, *dbus.Error) {
	b.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.backup.restore"); err != nil {
		return 0, err
	}
	cfg, err := findBackupConfigBySnapshot(snapshotID)
	if err != nil {
		return 0, dbus.MakeFailedError(err)
	}

	return b.startTransaction("Restauração: "+snapshotID, b.emitRestoreProgress, b.emitRestoreFinished, func(report progressFunc) error {
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

func (b *BackupService) DeleteConfig(sender dbus.Sender, configID string) *dbus.Error {
	b.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.backup.configure"); err != nil {
		return err
	}
	if err := removeBackupSystemdUnits(configID); err != nil {
		return dbus.MakeFailedError(err)
	}
	if err := os.Remove(backupConfigPath(configID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return dbus.MakeFailedError(err)
	}
	if err := os.Remove(backupPasswordPath(configID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return dbus.MakeFailedError(err)
	}
	_ = deleteBackupPasswordSecret(configID)
	resetBackupFailureCount(configID)
	return nil
}

func RunBackupJob(configID string, report progressFunc) error {
	cfg, err := readBackupConfig(configID)
	if err != nil {
		return err
	}
	if cfg.Frequency == "on-connect" && !backupDestinationIsAvailable(cfg) {
		if report != nil {
			report(100, "Destino indisponível; backup adiado até a próxima conexão")
		}
		return nil
	}
	if err := ensureResticRepository(cfg, report); err != nil {
		if errors.Is(err, errBackupDeferred) {
			if report != nil {
				report(100, "Destino indisponível; backup adiado até a próxima conexão")
			}
			return nil
		}
		return err
	}
	args := []string{"backup"}
	args = append(args, cfg.Paths...)
	return runResticCommand(cfg, args, report, "Iniciando backup...", "Backup concluído")
}

func resticSnapshotPaths(cfg BackupConfig, snapshotID string) ([]string, error) {
	destination, err := backupDestinationResolved(cfg)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("restic", "-r", destination, "ls", snapshotID)
	cmd.Env = backupCommandEnv(cfg.Id)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("restic ls: %w — %s", err, strings.TrimSpace(string(out)))
	}

	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "snapshot ") || strings.HasPrefix(line, "repository ") {
			continue
		}
		if strings.Contains(line, "  ") {
			line = strings.TrimSpace(strings.SplitN(line, "  ", 2)[1])
		}
		line = strings.TrimPrefix(line, "/")
		if line != "" {
			paths = append(paths, line)
		}
	}
	return filterEmpty(paths), nil
}

func (b *BackupService) ListSnapshotPaths(configID, snapshotID string) ([]string, *dbus.Error) {
	b.activity.Touch()
	cfg, err := readBackupConfig(configID)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	if err := ensureResticRepository(cfg, nil); err != nil {
		if errors.Is(err, errBackupDeferred) {
			return nil, nil
		}
		return nil, dbus.MakeFailedError(err)
	}
	paths, err := resticSnapshotPaths(cfg, snapshotID)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	return paths, nil
}

func (b *BackupService) RestoreItems(sender dbus.Sender, snapshotID, targetPath, mode string, paths []string) (uint32, *dbus.Error) {
	b.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.backup.restore"); err != nil {
		return 0, err
	}
	cfg, err := findBackupConfigBySnapshot(snapshotID)
	if err != nil {
		return 0, dbus.MakeFailedError(err)
	}
	return b.startTransaction("Restauração: "+snapshotID, b.emitRestoreProgress, b.emitRestoreFinished, func(report progressFunc) error {
		if err := ensureResticRepository(cfg, report); err != nil {
			if errors.Is(err, errBackupDeferred) {
				return err
			}
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
		args := []string{"restore", snapshotID, "--target", restoreTarget}
		for _, path := range filterEmpty(paths) {
			args = append(args, "--include", path)
		}
		return runResticCommand(cfg, args, report, "Iniciando restauração...", "Restauração concluída")
	}), nil
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

func (b *BackupService) emitBackupAlert(configID string, consecutiveFailures int, message string) error {
	return b.conn.Emit(ObjectPath, BusName+".Backup.BackupAlert", configID, consecutiveFailures, message)
}

func (b *BackupService) startTransaction(
	why string,
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
		err := withShutdownInhibit(why, func() error { return work(report) })
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
	cfg.DestinationUUID = strings.TrimSpace(cfg.DestinationUUID)
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
	if cfg.DestinationUUID != "" && strings.HasPrefix(cfg.Destination, "/") {
		cfg.Destination = strings.TrimLeft(cfg.Destination, "/")
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
	for _, dir := range []string{backupStateDir(), backupConfigsDir(), backupPasswordsDir(), backupFailuresDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func readBackupFailureCount(id string) (int, error) {
	data, err := os.ReadFile(backupFailureCountPath(id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	var count struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(data, &count); err != nil {
		return 0, err
	}
	if count.Count < 0 {
		return 0, nil
	}
	return count.Count, nil
}

func writeBackupFailureCount(id string, count int) error {
	if count < 0 {
		count = 0
	}
	if err := os.MkdirAll(backupFailuresDir(), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		Count int `json:"count"`
	}{Count: count})
	if err != nil {
		return err
	}
	return os.WriteFile(backupFailureCountPath(id), data, 0o600)
}

func resetBackupFailureCount(id string) {
	_ = os.Remove(backupFailureCountPath(id))
}

func incrementBackupFailureCount(id string) (int, error) {
	count, err := readBackupFailureCount(id)
	if err != nil {
		return 0, err
	}
	count++
	if err := writeBackupFailureCount(id, count); err != nil {
		return 0, err
	}
	return count, nil
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
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	password := fmt.Sprintf("%x", buf)
	if secretToolAvailable() {
		if err := storeBackupPasswordSecret(id, password); err == nil {
			_ = os.Remove(backupPasswordPath(id))
			return nil
		}
	}
	return os.WriteFile(backupPasswordPath(id), []byte(password), 0o600)
}

func resticPasswordFile(id string) string {
	return backupPasswordPath(id)
}

func resticPasswordCommand(id string) string {
	return fmt.Sprintf("secret-tool lookup service vega backup-id %s", id)
}

func backupCommandEnv(id string) []string {
	if secretToolAvailable() {
		if password, err := lookupBackupPasswordSecret(id); err == nil && password != "" {
			return append(os.Environ(), "RESTIC_PASSWORD_COMMAND="+resticPasswordCommand(id))
		}
	}
	return append(os.Environ(), "RESTIC_PASSWORD_FILE="+resticPasswordFile(id))
}

func storeBackupPasswordSecret(id, password string) error {
	cmd := exec.Command("secret-tool", "store", "--label=Vega restic password", "service", "vega", "backup-id", id)
	cmd.Stdin = strings.NewReader(password)
	return cmd.Run()
}

func lookupBackupPasswordSecret(id string) (string, error) {
	cmd := exec.Command("secret-tool", "lookup", "service", "vega", "backup-id", id)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("secret-tool lookup: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func deleteBackupPasswordSecret(id string) error {
	cmd := exec.Command("secret-tool", "clear", "service", "vega", "backup-id", id)
	return cmd.Run()
}

func ensureResticRepository(cfg BackupConfig, report progressFunc) error {
	if !resticAvailable() {
		return errBackupUnavailable
	}
	if cfg.Frequency == "on-connect" && !backupDestinationIsAvailable(cfg) {
		return errBackupDeferred
	}
	destination, err := backupDestinationResolved(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	cmd := exec.Command("restic", "-r", destination, "snapshots", "--json")
	cmd.Env = backupCommandEnv(cfg.Id)
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "is missing") || strings.Contains(string(out), "repository") {
			initCmd := exec.Command("restic", "-r", destination, "init")
			initCmd.Env = backupCommandEnv(cfg.Id)
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
	destination, err := backupDestinationResolved(cfg)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("restic", "-r", destination, "snapshots", "--json")
	cmd.Env = backupCommandEnv(cfg.Id)
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
	sort.SliceStable(outRows, func(i, j int) bool {
		if outRows[i].Timestamp == outRows[j].Timestamp {
			return outRows[i].Id > outRows[j].Id
		}
		return outRows[i].Timestamp > outRows[j].Timestamp
	})
	return outRows, nil
}

func runResticCommand(cfg BackupConfig, args []string, report progressFunc, startMsg, doneMsg string) error {
	report(0, startMsg)

	destination, err := backupDestinationResolved(cfg)
	if err != nil {
		return err
	}
	cmd := exec.Command("restic", append([]string{"-r", destination}, args...)...)
	cmd.Env = backupCommandEnv(cfg.Id)
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

func writeBackupSystemdUnits(cfg BackupConfig) error {
	if cfg.Frequency == "manual" {
		return nil
	}
	if !commandAvailable("systemctl") {
		return fmt.Errorf("systemd não está disponível")
	}

	if err := os.MkdirAll(backupSystemdDir, 0o755); err != nil {
		return err
	}

	serviceName := backupServiceUnitName(cfg.Id)
	timerName := backupTimerUnitName(cfg.Id)
	servicePath := filepath.Join(backupSystemdDir, serviceName)
	timerPath := filepath.Join(backupSystemdDir, timerName)
	pathName := backupPathUnitName(cfg.Id)
	pathPath := filepath.Join(backupSystemdDir, pathName)

	serviceUnit := fmt.Sprintf(`[Unit]
Description=Vega backup job for %s

[Service]
Type=oneshot
ExecStart=/usr/lib/vega/vegad backup run %s
# Backups can run long against slow/remote destinations. The shutdown
# inhibitor (see internal/dbusserver/inhibit.go) gives logind a short grace
# window while this runs. It does not guarantee completion of a long backup;
# restic operations must remain resumable after interruption.
TimeoutStopSec=1800
`, cfg.Id, cfg.Id)
	if err := os.WriteFile(servicePath, []byte(serviceUnit), 0o644); err != nil {
		return err
	}

	if cfg.Frequency == "daily" || cfg.Frequency == "weekly" {
		timerUnit := fmt.Sprintf(`[Unit]
Description=Vega backup timer for %s

[Timer]
OnCalendar=%s
Persistent=true
Unit=%s

[Install]
WantedBy=timers.target
`, cfg.Id, backupTimerCalendar(cfg.Frequency), serviceName)
		if err := os.WriteFile(timerPath, []byte(timerUnit), 0o644); err != nil {
			return err
		}
	} else if cfg.Frequency == "on-connect" {
		pathUnit := fmt.Sprintf(`[Unit]
Description=Vega backup path trigger for %s

[Path]
PathExists=%s
Unit=%s

[Install]
WantedBy=multi-user.target
`, cfg.Id, backupPathTrigger(cfg), serviceName)
		if err := os.WriteFile(pathPath, []byte(pathUnit), 0o644); err != nil {
			return err
		}
	}

	if err := runCommand("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if cfg.Frequency == "daily" || cfg.Frequency == "weekly" {
		return runCommand("systemctl", "enable", "--now", timerName)
	}
	if cfg.Frequency == "on-connect" {
		return runCommand("systemctl", "enable", "--now", pathName)
	}
	return nil
}

func removeBackupSystemdUnits(configID string) error {
	if !commandAvailable("systemctl") {
		return nil
	}
	timerName := backupTimerUnitName(configID)
	pathName := backupPathUnitName(configID)
	serviceName := backupServiceUnitName(configID)
	_ = runCommand("systemctl", "disable", "--now", timerName)
	_ = runCommand("systemctl", "disable", "--now", pathName)
	_ = os.Remove(filepath.Join(backupSystemdDir, timerName))
	_ = os.Remove(filepath.Join(backupSystemdDir, pathName))
	_ = os.Remove(filepath.Join(backupSystemdDir, serviceName))
	return runCommand("systemctl", "daemon-reload")
}

func backupServiceUnitName(id string) string {
	return "vega-backup-" + id + ".service"
}

func backupTimerUnitName(id string) string {
	return "vega-backup-" + id + ".timer"
}

func backupPathUnitName(id string) string {
	return "vega-backup-" + id + ".path"
}

func backupTimerCalendar(freq string) string {
	switch freq {
	case "weekly":
		return "weekly"
	default:
		return "daily"
	}
}

func newLineScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Split(bufio.ScanLines)
	return scanner
}
