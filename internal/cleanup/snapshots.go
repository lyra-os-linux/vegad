package cleanup

import (
	"bytes"
	"context"
	"encoding/csv"
	"strconv"
	"strings"
	"time"
)

type snapperSnapshot struct {
	cleanup   string
	date      time.Time
	important bool
}

func parseSnapperCSV(out []byte) ([][]string, error) {
	reader := csv.NewReader(bytes.NewReader(out))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[1:], nil
}

func parseSnapperConfig(out []byte) (map[string]string, error) {
	rows, err := parseSnapperCSV(out)
	if err != nil {
		return nil, err
	}
	config := map[string]string{}
	for _, row := range rows {
		if len(row) == 2 {
			config[row[0]] = row[1]
		}
	}
	return config, nil
}

// parseSnapperList reads "snapper --iso --machine-readable csv list
// --columns number,type,date,cleanup,userdata"; dates are local time.
func parseSnapperList(out []byte, location *time.Location) ([]snapperSnapshot, error) {
	rows, err := parseSnapperCSV(out)
	if err != nil {
		return nil, err
	}
	var snapshots []snapperSnapshot
	for _, row := range rows {
		if len(row) < 5 || row[0] == "0" || row[3] == "" {
			continue
		}
		date, err := time.ParseInLocation("2006-01-02 15:04:05", row[2], location)
		if err != nil {
			continue
		}
		snapshots = append(snapshots, snapperSnapshot{
			cleanup:   row[3],
			date:      date,
			important: strings.Contains(row[4], "important=yes"),
		})
	}
	return snapshots, nil
}

// upperLimit reads a snapper limit, "10" or the range "2-10".
func upperLimit(value string) int {
	if _, high, ok := strings.Cut(value, "-"); ok {
		value = high
	}
	limit, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || limit < 0 {
		return -1
	}
	return limit
}

// excess is how many of the old enough snapshots exceed the limit; snapper
// removes at least that many, never one younger than the minimum age.
func excess(total, old, limit int) int {
	if limit < 0 || total <= limit {
		return 0
	}
	return min(total-limit, old)
}

// snapshotsToClean is a lower bound of what "snapper cleanup number" and
// "snapper cleanup timeline" remove: the timeline algorithm keeps one
// snapshot per hour, day, week… and one snapshot may fill several of those
// slots, so more than counted here may go.
func snapshotsToClean(config map[string]string, snapshots []snapperSnapshot, now time.Time) int {
	minAge := func(key string) time.Time {
		seconds, err := strconv.Atoi(config[key])
		if err != nil {
			seconds = 1800
		}
		return now.Add(-time.Duration(seconds) * time.Second)
	}
	count := 0
	if config["NUMBER_CLEANUP"] == "yes" {
		cut := minAge("NUMBER_MIN_AGE")
		var normal, normalOld, important, importantOld int
		for _, s := range snapshots {
			if s.cleanup != "number" {
				continue
			}
			if s.important {
				important++
				if s.date.Before(cut) {
					importantOld++
				}
			} else {
				normal++
				if s.date.Before(cut) {
					normalOld++
				}
			}
		}
		count += excess(normal, normalOld, upperLimit(config["NUMBER_LIMIT"]))
		count += excess(important, importantOld, upperLimit(config["NUMBER_LIMIT_IMPORTANT"]))
	}
	if config["TIMELINE_CLEANUP"] == "yes" {
		cut := minAge("TIMELINE_MIN_AGE")
		limit := 0
		for _, key := range []string{"HOURLY", "DAILY", "WEEKLY", "MONTHLY", "QUARTERLY", "YEARLY"} {
			if value := upperLimit(config["TIMELINE_LIMIT_"+key]); value > 0 {
				limit += value
			}
		}
		var total, old int
		for _, s := range snapshots {
			if s.cleanup == "timeline" {
				total++
				if s.date.Before(cut) {
					old++
				}
			}
		}
		count += excess(total, old, limit)
	}
	return count
}

func measureSnapshots(ctx context.Context, env *Env) (measurement, error) {
	if reason := missingTool(env, "snapper", "snapper"); reason != "" {
		return measurement{reason: reason}, nil
	}
	out, code, err := env.Run(ctx, "", "snapper", "--machine-readable", "csv", "get-config")
	if err != nil {
		return measurement{}, err
	}
	if code != 0 {
		return measurement{reason: "not-configured"}, nil
	}
	config, err := parseSnapperConfig(out)
	if err != nil {
		return measurement{}, err
	}
	out, err = runOK(ctx, env, "", "snapper", "--iso", "--machine-readable", "csv", "list", "--columns", "number,type,date,cleanup,userdata")
	if err != nil {
		return measurement{}, err
	}
	snapshots, err := parseSnapperList(out, time.Local)
	if err != nil {
		return measurement{}, err
	}
	count := snapshotsToClean(config, snapshots, env.Now())
	return measurement{count: count, sizeUnknown: true, minimum: true}, nil
}

// cleanSnapshots applies the configured policy; nothing beyond it.
func cleanSnapshots(ctx context.Context, env *Env, _ measurement) error {
	for _, algorithm := range []string{"number", "timeline"} {
		if _, err := runOK(ctx, env, "", "snapper", "cleanup", algorithm); err != nil {
			return err
		}
	}
	return nil
}
