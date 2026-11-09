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
	seqID  string // seqnum id of the file; empty for renamed (~) files
	seq    uint64 // seqnum of the first entry
	size   uint64
}

// parseArchivedName reads the head realtime that journald writes into the
// name of an archived file:
//
//	system@<seqnum id>-<head seqnum>-<head realtime>.journal
//	system@<head realtime>-<random>.journal~   (renamed after corruption)
//
// Active files (system.journal) have no "@" and are never candidates.
func parseArchivedName(name string) (file journalFile, ok bool) {
	prefix, rest, found := strings.Cut(name, "@")
	if !found || prefix == "" {
		return journalFile{}, false
	}
	file.prefix = prefix
	var realtime string
	switch {
	case strings.HasSuffix(rest, ".journal"):
		fields := strings.Split(strings.TrimSuffix(rest, ".journal"), "-")
		if len(fields) != 3 || len(fields[0]) != 32 {
			return journalFile{}, false
		}
		seq, err := strconv.ParseUint(fields[1], 16, 64)
		if err != nil {
			return journalFile{}, false
		}
		file.seqID, file.seq, realtime = strings.ToLower(fields[0]), seq, fields[2]
	case strings.HasSuffix(rest, ".journal~"):
		fields := strings.Split(strings.TrimSuffix(rest, ".journal~"), "-")
		if len(fields) != 2 {
			return journalFile{}, false
		}
		realtime = fields[0]
	default:
		return journalFile{}, false
	}
	head, err := strconv.ParseUint(realtime, 16, 64)
	if err != nil || head == 0 {
		return journalFile{}, false
	}
	file.head = head
	return file, true
}

// bootCutoff is the first entry of the boot the cleanup must keep: its
// realtime and, when journalctl reports them, its seqnum and seqnum id.
type bootCutoff struct {
	realtime uint64
	seqID    string
	seq      uint64
}

// journalCandidates returns, oldest first, the archived files whose removal
// brings the journal down to journalLimit without reaching the previous
// boot: a file is eligible only when the next file of the same prefix starts
// before the cutoff, so every entry it holds is older than that boot.
//
// Files in the boot's seqnum space are ordered and compared by seqnum, which
// journald increments regardless of the clock (vegad#126): a boot that began
// with the clock ahead cannot make its own files look older. Renamed (~)
// files and older seqnum spaces only carry a time, so they keep the realtime
// rule.
func journalCandidates(files []journalFile, total uint64, cut bootCutoff) []journalFile {
	bySeq, byTime := map[string][]journalFile{}, map[string][]journalFile{}
	for _, file := range files {
		if cut.seqID != "" && file.seqID == cut.seqID {
			bySeq[file.prefix] = append(bySeq[file.prefix], file)
		} else {
			byTime[file.prefix] = append(byTime[file.prefix], file)
		}
	}
	var eligible []journalFile
	for _, list := range bySeq {
		sort.Slice(list, func(i, j int) bool { return list[i].seq < list[j].seq })
		for i := 0; i+1 < len(list); i++ {
			if list[i+1].seq <= cut.seq {
				eligible = append(eligible, list[i])
			}
		}
	}
	for _, list := range byTime {
		sort.Slice(list, func(i, j int) bool { return list[i].head < list[j].head })
		for i := 0; i+1 < len(list); i++ {
			if list[i+1].head <= cut.realtime {
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
func previousBootStart(ctx context.Context, env *Env) (bootCutoff, error) {
	out, err := runOK(ctx, env, "", "journalctl", "--list-boots", "-o", "json", "--no-pager")
	if err != nil {
		return bootCutoff{}, err
	}
	var boots []bootEntry
	if err := json.Unmarshal(out, &boots); err != nil {
		return bootCutoff{}, err
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
	switch {
	case previous != 0:
		return withBootSeqnum(ctx, env, "-1", previous), nil
	case current != 0:
		return withBootSeqnum(ctx, env, "0", current), nil
	}
	return bootCutoff{}, errors.New("journalctl não listou o boot atual")
}

// withBootSeqnum adds the seqnum of the boot's first entry (systemd ≥ 254).
// Without it the cutoff keeps only the realtime.
func withBootSeqnum(ctx context.Context, env *Env, boot string, realtime uint64) bootCutoff {
	cut := bootCutoff{realtime: realtime}
	out, err := runOK(ctx, env, "", "journalctl", "-b", boot, "-o", "json", "--no-pager",
		"--lines=+1", "--output-fields=__SEQNUM,__SEQNUM_ID")
	if err != nil {
		return cut
	}
	var first struct {
		Seq   string `json:"__SEQNUM"`
		SeqID string `json:"__SEQNUM_ID"`
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if json.Unmarshal([]byte(line), &first) != nil || len(first.SeqID) != 32 {
		return cut
	}
	seq, err := strconv.ParseUint(first.Seq, 10, 64)
	if err != nil {
		return cut
	}
	cut.seqID, cut.seq = strings.ToLower(first.SeqID), seq
	return cut
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
			if file, ok := parseArchivedName(name); ok {
				// The machine-id directory is part of the rotation chain.
				file.path, file.prefix, file.size = filepath.Join(base, name), dir.Name()+"/"+file.prefix, size
				files = append(files, file)
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
