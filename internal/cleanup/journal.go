package cleanup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const journalDir = "/var/log/journal"

// journalLimit is what the journal keeps (decided 29/10/2026); tests lower it.
var journalLimit uint64 = 500 << 20

type journalFile struct {
	path   string
	prefix string // "system", "user-1001"; files rotate per prefix
	head   uint64 // realtime of the first entry, µs
	size   uint64
}

// parseArchivedName reads the head realtime that journald writes into the
// name of an archived file:
//
//	system@<seqnum id>-<head seqnum>-<head realtime>.journal
//	system@<head realtime>-<random>.journal~   (renamed after corruption)
//
// Active files (system.journal) have no "@" and are never candidates.
func parseArchivedName(name string) (prefix string, head uint64, ok bool) {
	prefix, rest, found := strings.Cut(name, "@")
	if !found || prefix == "" {
		return "", 0, false
	}
	var fields []string
	switch {
	case strings.HasSuffix(rest, ".journal"):
		fields = strings.Split(strings.TrimSuffix(rest, ".journal"), "-")
		if len(fields) != 3 {
			return "", 0, false
		}
		fields = fields[2:]
	case strings.HasSuffix(rest, ".journal~"):
		fields = strings.Split(strings.TrimSuffix(rest, ".journal~"), "-")
		if len(fields) != 2 {
			return "", 0, false
		}
	default:
		return "", 0, false
	}
	head, err := strconv.ParseUint(fields[0], 16, 64)
	if err != nil || head == 0 {
		return "", 0, false
	}
	return prefix, head, true
}

// journalCandidates returns, oldest first, the archived files whose removal
// brings the journal down to journalLimit without reaching the previous
// boot: a file is eligible only when the next file of the same prefix starts
// before cutoff, so every entry it holds is older than that boot.
func journalCandidates(files []journalFile, total, cutoff uint64) []journalFile {
	byPrefix := map[string][]journalFile{}
	for _, file := range files {
		byPrefix[file.prefix] = append(byPrefix[file.prefix], file)
	}
	var eligible []journalFile
	for _, list := range byPrefix {
		sort.Slice(list, func(i, j int) bool { return list[i].head < list[j].head })
		for i := 0; i+1 < len(list); i++ {
			if list[i+1].head <= cutoff {
				eligible = append(eligible, list[i])
			}
		}
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].head < eligible[j].head })
	var chosen []journalFile
	for _, file := range eligible {
		if total <= journalLimit {
			break
		}
		chosen = append(chosen, file)
		total -= min(file.size, total)
	}
	return chosen
}

type bootEntry struct {
	Index      int    `json:"index"`
	FirstEntry uint64 `json:"first_entry"`
}

// previousBootStart is the first entry of the previous boot, or of the
// current one when there is no previous boot.
func previousBootStart(ctx context.Context, env *Env) (uint64, error) {
	out, err := runOK(ctx, env, "", "journalctl", "--list-boots", "-o", "json", "--no-pager")
	if err != nil {
		return 0, err
	}
	var boots []bootEntry
	if err := json.Unmarshal(out, &boots); err != nil {
		return 0, err
	}
	var current, previous uint64
	for _, boot := range boots {
		switch boot.Index {
		case 0:
			current = boot.FirstEntry
		case -1:
			previous = boot.FirstEntry
		}
	}
	if previous != 0 {
		return previous, nil
	}
	if current != 0 {
		return current, nil
	}
	return 0, errors.New("journalctl não listou o boot atual")
}

func journalState(ctx context.Context, env *Env) ([]journalFile, uint64, error) {
	dirs, err := os.ReadDir(env.path(journalDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var files []journalFile
	var total uint64
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		base := filepath.Join(env.path(journalDir), dir.Name())
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".journal") && !strings.HasSuffix(name, ".journal~") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			size := allocated(info)
			total += size
			if prefix, head, ok := parseArchivedName(name); ok {
				// The machine-id directory is part of the rotation chain.
				files = append(files, journalFile{filepath.Join(base, name), dir.Name() + "/" + prefix, head, size})
			}
		}
	}
	return files, total, nil
}

func journalPlan(ctx context.Context, env *Env) ([]journalFile, error) {
	files, total, err := journalState(ctx, env)
	if err != nil || total <= journalLimit {
		return nil, err
	}
	cutoff, err := previousBootStart(ctx, env)
	if err != nil {
		return nil, err
	}
	return journalCandidates(files, total, cutoff), nil
}

func measureJournal(ctx context.Context, env *Env) (measurement, error) {
	if reason := missingTool(env, "journalctl", "systemd"); reason != "" {
		return measurement{reason: reason}, nil
	}
	plan, err := journalPlan(ctx, env)
	if err != nil {
		return measurement{}, err
	}
	m := measurement{count: len(plan)}
	for _, file := range plan {
		m.bytes += file.size
	}
	return m, nil
}

// cleanJournal removes the planned archived files, as journalctl --vacuum
// does, but only those the plan proved older than the previous boot.
func cleanJournal(ctx context.Context, env *Env, _ measurement) error {
	plan, err := journalPlan(ctx, env)
	if err != nil {
		return err
	}
	var errs []error
	for _, file := range plan {
		if err := os.Remove(file.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
