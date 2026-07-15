package dbusserver

import (
	"errors"
	"fmt"
	"hash/fnv"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var errTimeshiftUnavailable = errors.New("timeshift não está disponível neste sistema")

var timeshiftNamePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}$`)
var timeshiftNameSearchPattern = regexp.MustCompile(`\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}`)

func timeshiftInstalled() bool {
	_, err := exec.LookPath("timeshift")
	return err == nil
}

func timeshiftCombinedOutput(args ...string) ([]byte, error) {
	cmd := exec.Command("timeshift", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("timeshift %s: %w — %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func timeshiftSnapshotID(name string) uint32 {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(name))
	return hash.Sum32()
}

func parseTimeshiftSnapshots(output string) []SnapshotInfo {
	var snapshots []SnapshotInfo
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		nameIndex := -1
		for index, field := range fields {
			if timeshiftNamePattern.MatchString(field) {
				nameIndex = index
				break
			}
		}
		if nameIndex < 0 {
			continue
		}
		name := fields[nameIndex]
		parsed, _ := time.ParseInLocation("2006-01-02_15-04-05", name, time.Local)
		tagsEnd := nameIndex + 1
		for tagsEnd < len(fields) && isTimeshiftTag(fields[tagsEnd]) {
			tagsEnd++
		}
		description := strings.Join(fields[tagsEnd:], " ")
		trigger := strings.Join(fields[nameIndex+1:tagsEnd], " ")
		if trigger == "" {
			trigger = "timeshift"
		}
		snapshots = append(snapshots, SnapshotInfo{
			Id:          timeshiftSnapshotID(name),
			Timestamp:   parsed.Unix(),
			Trigger:     trigger,
			Description: description,
		})
	}
	sort.SliceStable(snapshots, func(i, j int) bool {
		return snapshots[i].Timestamp > snapshots[j].Timestamp
	})
	return snapshots
}

func isTimeshiftTag(value string) bool {
	return len(value) == 1 && strings.Contains("OBHDWM", value)
}

func listTimeshiftSnapshots() ([]SnapshotInfo, error) {
	if !timeshiftInstalled() {
		return nil, errTimeshiftUnavailable
	}
	out, err := timeshiftCombinedOutput("--list", "--scripted")
	if err != nil {
		return nil, err
	}
	return parseTimeshiftSnapshots(string(out)), nil
}

func timeshiftSnapshotName(id uint32) (string, error) {
	snapshots, err := listTimeshiftSnapshots()
	if err != nil {
		return "", err
	}
	for _, snapshot := range snapshots {
		name := time.Unix(snapshot.Timestamp, 0).Format("2006-01-02_15-04-05")
		if timeshiftSnapshotID(name) == id {
			return name, nil
		}
	}
	return "", fmt.Errorf("snapshot Timeshift %s não encontrado", strconv.FormatUint(uint64(id), 10))
}

func createTimeshiftSnapshot(description string) (uint32, error) {
	out, err := timeshiftCombinedOutput("--create", "--comments", description, "--tags", "O", "--scripted", "--yes")
	if err != nil {
		return 0, err
	}
	for _, match := range timeshiftNameSearchPattern.FindAllString(string(out), -1) {
		return timeshiftSnapshotID(match), nil
	}
	snapshots, listErr := listTimeshiftSnapshots()
	if listErr != nil || len(snapshots) == 0 {
		return 0, fmt.Errorf("timeshift create: não foi possível identificar o snapshot criado")
	}
	return snapshots[0].Id, nil
}

func deleteTimeshiftSnapshot(id uint32) error {
	name, err := timeshiftSnapshotName(id)
	if err != nil {
		return err
	}
	_, err = timeshiftCombinedOutput("--delete", "--snapshot", name, "--scripted", "--yes")
	return err
}

func rollbackTimeshiftSnapshot(id uint32) error {
	name, err := timeshiftSnapshotName(id)
	if err != nil {
		return err
	}
	_, err = timeshiftCombinedOutput("--restore", "--snapshot", name, "--scripted", "--yes")
	return err
}
