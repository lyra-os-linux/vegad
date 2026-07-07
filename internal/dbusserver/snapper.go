package dbusserver

import (
	"encoding/csv"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

const snapperConfig = "root"

var errSnapperUnavailable = errors.New("snapper não está disponível neste sistema")

func snapperInstalled() bool {
	_, err := exec.LookPath("snapper")
	return err == nil
}

func snapperCommand(args ...string) *exec.Cmd {
	fullArgs := append([]string{"-c", snapperConfig}, args...)
	return exec.Command("snapper", fullArgs...)
}

func snapperCombinedOutput(args ...string) ([]byte, error) {
	cmd := snapperCommand(args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("snapper %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func createSnapperSnapshot(kind, description string, preNumber ...uint32) (uint32, error) {
	if !snapperInstalled() {
		return 0, errSnapperUnavailable
	}

	args := []string{"create", "--type", kind, "--description", description, "--print-number"}
	if len(preNumber) > 0 {
		args = append(args, "--pre-number", strconv.FormatUint(uint64(preNumber[0]), 10))
	}

	out, err := snapperCombinedOutput(args...)
	if err != nil {
		return 0, err
	}

	n, parseErr := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 32)
	if parseErr != nil {
		return 0, fmt.Errorf("snapper create: saída inesperada %q: %w", strings.TrimSpace(string(out)), parseErr)
	}
	return uint32(n), nil
}

func parseSnapperSnapshots(text string) ([]SnapshotInfo, error) {
	reader := csv.NewReader(strings.NewReader(text))
	reader.Comma = ';'
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil || len(rows) == 0 {
		reader = csv.NewReader(strings.NewReader(text))
		reader.Comma = ','
		reader.FieldsPerRecord = -1
		rows, err = reader.ReadAll()
		if err != nil || len(rows) == 0 {
			return parseSnapperTable(text), nil
		}
	}

	header := map[string]int{}
	for idx, name := range rows[0] {
		header[strings.ToLower(strings.TrimSpace(name))] = idx
	}

	get := func(row []string, names ...string) string {
		for _, name := range names {
			if idx, ok := header[name]; ok && idx < len(row) {
				return strings.TrimSpace(row[idx])
			}
		}
		return ""
	}

	var snapshots []SnapshotInfo
	for _, row := range rows[1:] {
		idText := get(row, "number", "num", "#", "id")
		if idText == "" {
			continue
		}
		id, err := strconv.ParseUint(idText, 10, 32)
		if err != nil {
			continue
		}

		ts := parseSnapperTimestamp(get(row, "date"), get(row, "time"), get(row, "timestamp"))
		snapshots = append(snapshots, SnapshotInfo{
			Id:          uint32(id),
			Timestamp:   ts,
			Trigger:     firstNonEmpty(get(row, "type", "cleanup"), get(row, "kind")),
			Description: firstNonEmpty(get(row, "description", "desc"), ""),
		})
	}
	return snapshots, nil
}

func parseSnapperTable(text string) []SnapshotInfo {
	var snapshots []SnapshotInfo
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		if !strings.Contains(line, "|") {
			continue
		}
		fields := splitSnapperRow(line)
		if len(fields) < 4 {
			continue
		}
		if _, err := strconv.ParseUint(fields[0], 10, 32); err != nil {
			continue
		}
		id, _ := strconv.ParseUint(fields[0], 10, 32)
		trigger := ""
		description := ""
		if len(fields) > 1 {
			trigger = fields[1]
		}
		if len(fields) > 4 {
			description = strings.Join(fields[4:], " | ")
		}
		snapshots = append(snapshots, SnapshotInfo{
			Id:          uint32(id),
			Timestamp:   parseSnapperTimestamp(fields[2], fields[3], ""),
			Trigger:     trigger,
			Description: description,
		})
	}
	return snapshots
}

func splitSnapperRow(line string) []string {
	parts := strings.Split(line, "|")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func parseSnapperTimestamp(dateText, timeText, tsText string) int64 {
	if tsText != "" {
		if ts, err := strconv.ParseInt(tsText, 10, 64); err == nil {
			return ts
		}
	}
	candidates := []string{
		strings.TrimSpace(dateText + " " + timeText),
		strings.TrimSpace(dateText),
	}
	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
		"02.01.2006 15:04:05",
		"02.01.2006 15:04",
		"02.01.2006",
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		for _, layout := range layouts {
			if t, err := time.ParseInLocation(layout, candidate, time.Local); err == nil {
				return t.Unix()
			}
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func listSnapperSnapshots() ([]SnapshotInfo, error) {
	if !snapperInstalled() {
		return nil, errSnapperUnavailable
	}

	out, err := snapperCombinedOutput("list", "--csvout")
	if err != nil {
		return nil, err
	}
	snapshots, err := parseSnapperSnapshots(string(out))
	if err != nil {
		return nil, err
	}

	sort.SliceStable(snapshots, func(i, j int) bool {
		if snapshots[i].Timestamp == snapshots[j].Timestamp {
			return snapshots[i].Id < snapshots[j].Id
		}
		return snapshots[i].Timestamp > snapshots[j].Timestamp
	})
	return snapshots, nil
}

func snapperDiffLines(snapshotID uint32) ([]string, error) {
	if !snapperInstalled() {
		return nil, errSnapperUnavailable
	}

	targets := []string{
		fmt.Sprintf("%d..0", snapshotID),
		fmt.Sprintf("%d..current", snapshotID),
	}
	var lastErr error
	for _, target := range targets {
		for _, command := range [][]string{{"status", target}, {"diff", target}} {
			out, err := snapperCombinedOutput(command...)
			if err != nil {
				lastErr = err
				continue
			}
			lines := strings.Split(string(out), "\n")
			var cleaned []string
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line != "" {
					cleaned = append(cleaned, line)
				}
			}
			if len(cleaned) > 0 {
				return cleaned, nil
			}
		}
	}
	if lastErr != nil {
		return []string{lastErr.Error()}, nil
	}
	return []string{"Nenhuma diferença encontrada."}, nil
}

func rollbackSnapperSnapshot(snapshotID uint32) error {
	if !snapperInstalled() {
		return errSnapperUnavailable
	}
	_, err := snapperCombinedOutput("rollback", strconv.FormatUint(uint64(snapshotID), 10))
	return err
}

func setSnapperRetentionPolicy(keepCount uint32) error {
	if !snapperInstalled() {
		return errSnapperUnavailable
	}
	_, err := snapperCombinedOutput("set-config", fmt.Sprintf("NUMBER_LIMIT=%d", keepCount))
	return err
}
