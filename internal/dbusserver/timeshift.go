package dbusserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// timeshift.go is Snapshots' Debian/Ubuntu counterpart to snapper.go.
// Timeshift ships no D-Bus API either (same CLI-only situation as snapper),
// but is a materially different tool, not just a differently-named
// equivalent:
//
//   - It has no integer snapshot IDs like snapper's `--print-number` — see
//     timeshiftID below for how this backend fabricates one.
//   - In its default rsync mode (the realistic case on stock Ubuntu, which
//     ships ext4 rather than Btrfs) it requires a backup device to already
//     be configured through Timeshift's own first-run wizard —
//     timeshiftConfigured checks for that instead of assuming zero-config
//     like snapper on a Btrfs root.
//   - It has no per-package diff like `snapper diff` (it's a whole-filesystem
//     rsync/btrfs copy, not integrated with the package manager) —
//     DiffPackages' Timeshift path returns an explanatory message, not an
//     error, so the UI still shows something sensible.
//   - Retention is per schedule tier (count_daily/weekly/monthly/hourly/boot
//     in its JSON config), not a single NUMBER_LIMIT like snapper — see
//     setTimeshiftRetentionPolicy.

var errTimeshiftUnavailable = errors.New("timeshift não está disponível neste sistema")
var errTimeshiftNotConfigured = errors.New("timeshift está instalado mas não tem um dispositivo de backup configurado — rode o assistente do Timeshift primeiro")

const timeshiftConfigPath = "/etc/timeshift/timeshift.json"

func timeshiftInstalled() bool {
	return commandAvailable("timeshift")
}

func timeshiftConfigured() bool {
	data, err := os.ReadFile(timeshiftConfigPath)
	if err != nil {
		return false
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false
	}
	uuid, _ := cfg["backup_device_uuid"].(string)
	return strings.TrimSpace(uuid) != ""
}

func timeshiftCommand(args ...string) *exec.Cmd {
	// --scripted disables every interactive prompt Timeshift would
	// otherwise show — there is no terminal attached to answer one.
	fullArgs := append(append([]string{}, args...), "--scripted")
	cmd := exec.Command("timeshift", fullArgs...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd
}

func timeshiftCombinedOutput(args ...string) ([]byte, error) {
	cmd := timeshiftCommand(args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("timeshift %s: %w — %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// timeshiftNameRe matches a snapshot's "Name" column (Timeshift's own
// timestamp-based identifier, e.g. "2026-07-13_10-30-45") — locating it by
// pattern rather than fixed column offsets, since `timeshift --list`'s
// table is column-aligned with variable padding, not delimiter-separated.
var timeshiftNameRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}`)

const timeshiftNameLayout = "2006-01-02_15-04-05"

// timeshiftID fabricates a uint32 ID from a snapshot's name timestamp —
// Timeshift has no numeric ID of its own like snapper's snapshot number.
// Using the Unix timestamp (valid until 2106) instead of a synthetic
// index/cache means the ID is deterministic and doesn't go stale between
// a ListSnapshots call and a later Rollback/Delete call, even if other
// snapshots are created or removed in between.
func timeshiftID(name string) (uint32, error) {
	t, err := time.ParseInLocation(timeshiftNameLayout, name, time.Local)
	if err != nil {
		return 0, err
	}
	return uint32(t.Unix()), nil
}

func timeshiftTagLabel(tag string) string {
	switch tag {
	case "O":
		return "manual"
	case "B":
		return "boot"
	case "H":
		return "hourly"
	case "D":
		return "daily"
	case "W":
		return "weekly"
	case "M":
		return "monthly"
	default:
		return tag
	}
}

func parseTimeshiftSnapshots(out string) []SnapshotInfo {
	var snapshots []SnapshotInfo
	for _, line := range strings.Split(out, "\n") {
		loc := timeshiftNameRe.FindStringIndex(line)
		if loc == nil {
			continue
		}
		name := line[loc[0]:loc[1]]
		id, err := timeshiftID(name)
		if err != nil {
			continue
		}

		rest := strings.TrimSpace(line[loc[1]:])
		tag, description := "", ""
		if rest != "" {
			fields := strings.SplitN(rest, " ", 2)
			tag = fields[0]
			if len(fields) == 2 {
				description = strings.TrimSpace(fields[1])
			}
		}

		ts, _ := time.ParseInLocation(timeshiftNameLayout, name, time.Local)
		snapshots = append(snapshots, SnapshotInfo{
			Id:          id,
			Timestamp:   ts.Unix(),
			Trigger:     timeshiftTagLabel(tag),
			Description: description,
		})
	}
	sort.SliceStable(snapshots, func(i, j int) bool { return snapshots[i].Timestamp > snapshots[j].Timestamp })
	return snapshots
}

func listTimeshiftSnapshots() ([]SnapshotInfo, error) {
	if !timeshiftInstalled() {
		return nil, errTimeshiftUnavailable
	}
	if !timeshiftConfigured() {
		return nil, errTimeshiftNotConfigured
	}
	out, err := timeshiftCombinedOutput("--list")
	if err != nil {
		return nil, err
	}
	return parseTimeshiftSnapshots(string(out)), nil
}

// timeshiftNameForID re-lists snapshots and matches by the same Unix-time
// ID scheme timeshiftID produces, rather than caching name↔ID mappings
// that could go stale.
func timeshiftNameForID(id uint32) (string, error) {
	snapshots, err := listTimeshiftSnapshots()
	if err != nil {
		return "", err
	}
	for _, snap := range snapshots {
		if snap.Id == id {
			return time.Unix(snap.Timestamp, 0).In(time.Local).Format(timeshiftNameLayout), nil
		}
	}
	return "", fmt.Errorf("timeshift: snapshot %d não encontrado", id)
}

func createTimeshiftSnapshot(description string) (uint32, error) {
	if !timeshiftInstalled() {
		return 0, errTimeshiftUnavailable
	}
	if !timeshiftConfigured() {
		return 0, errTimeshiftNotConfigured
	}

	if _, err := timeshiftCombinedOutput("--create", "--comments", description, "--tags", "O"); err != nil {
		return 0, err
	}

	// Timeshift has no --print-number equivalent, so the just-created
	// snapshot is identified as the newest entry in a fresh listing.
	snapshots, err := listTimeshiftSnapshots()
	if err != nil {
		return 0, err
	}
	if len(snapshots) == 0 {
		return 0, fmt.Errorf("timeshift: snapshot criado mas não encontrado na listagem")
	}
	return snapshots[0].Id, nil
}

// rollbackTimeshiftSnapshot restores with --skip-grub: Timeshift's rsync
// restore rewrites files in place on the running root, which doesn't
// change the GRUB install location — reinstalling GRUB is Timeshift's
// safety net for restoring onto a different/replaced disk, not the
// in-place case this triggers from.
func rollbackTimeshiftSnapshot(id uint32) error {
	if !timeshiftInstalled() {
		return errTimeshiftUnavailable
	}
	name, err := timeshiftNameForID(id)
	if err != nil {
		return err
	}
	_, err = timeshiftCombinedOutput("--restore", "--snapshot", name, "--skip-grub", "--yes")
	return err
}

func deleteTimeshiftSnapshot(id uint32) error {
	if !timeshiftInstalled() {
		return errTimeshiftUnavailable
	}
	name, err := timeshiftNameForID(id)
	if err != nil {
		return err
	}
	_, err = timeshiftCombinedOutput("--delete", "--snapshot", name, "--yes")
	return err
}

// setTimeshiftRetentionPolicy maps the single "keep N" slider the UI
// exposes (modeled on snapper's one NUMBER_LIMIT) onto every one of
// Timeshift's per-schedule retention counts (count_daily/weekly/monthly/
// hourly/boot) uniformly — Timeshift has no single unified retention
// count, so this is a deliberate approximation, not a precise port.
// Schedule toggles (schedule_daily etc.) are left untouched.
func setTimeshiftRetentionPolicy(keepCount uint32) error {
	if !timeshiftInstalled() {
		return errTimeshiftUnavailable
	}
	if !timeshiftConfigured() {
		return errTimeshiftNotConfigured
	}

	data, err := os.ReadFile(timeshiftConfigPath)
	if err != nil {
		return fmt.Errorf("timeshift: lendo %s: %w", timeshiftConfigPath, err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("timeshift: parseando %s: %w", timeshiftConfigPath, err)
	}

	count := strconv.FormatUint(uint64(keepCount), 10)
	for _, key := range []string{"count_monthly", "count_weekly", "count_daily", "count_hourly", "count_boot"} {
		cfg[key] = count
	}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(timeshiftConfigPath, out, 0644)
}

// timeshiftDiffPackages has no real equivalent to snapper's package-aware
// diff — Timeshift is a whole-filesystem rsync/btrfs copy tool with no
// integration with the package manager, so there is nothing meaningful to
// report beyond saying so.
func timeshiftDiffPackages() []string {
	return []string{"Timeshift não oferece um diff de pacotes como o snapper — é uma cópia de arquivos, sem integração com o gerenciador de pacotes."}
}
