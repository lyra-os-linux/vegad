package diagnostics

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// journalEntry keeps the fields the logs test reads. MESSAGE and the
// coredump fields may be JSON strings, byte arrays (non-UTF-8) or null.
type journalEntry struct {
	Message    json.RawMessage `json:"MESSAGE"`
	Identifier json.RawMessage `json:"SYSLOG_IDENTIFIER"`
	Unit       json.RawMessage `json:"_SYSTEMD_UNIT"`
	MessageID  json.RawMessage `json:"MESSAGE_ID"`
	Exe        json.RawMessage `json:"COREDUMP_EXE"`
	Comm       json.RawMessage `json:"COREDUMP_COMM"`
	CoreUnit   json.RawMessage `json:"COREDUMP_UNIT"`
	UserUnit   json.RawMessage `json:"COREDUMP_USER_UNIT"`
	Signal     json.RawMessage `json:"COREDUMP_SIGNAL_NAME"`
}

func journalString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var data []byte
	var ints []int
	if json.Unmarshal(raw, &ints) == nil {
		for _, v := range ints {
			data = append(data, byte(v))
		}
		return strings.ToValidUTF8(string(data), "?")
	}
	return ""
}

func parseJournal(out []byte) []journalEntry {
	var entries []journalEntry
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		var entry journalEntry
		if json.Unmarshal(scanner.Bytes(), &entry) == nil {
			entries = append(entries, entry)
		}
	}
	return entries
}

type bootInfo struct {
	Index int `json:"index"`
}

var (
	hexPattern    = regexp.MustCompile(`0x[0-9a-fA-F]+|\b[0-9a-f]{8,}\b`)
	numberPattern = regexp.MustCompile(`[0-9]+`)
)

// normalizeMessage folds the parts that change between repeats (PIDs,
// addresses, counters) so identical errors deduplicate.
func normalizeMessage(text string) string {
	text = hexPattern.ReplaceAllString(text, "X")
	return numberPattern.ReplaceAllString(text, "N")
}

type noiseRule struct {
	code    string
	pattern *regexp.Regexp
}

// knownNoise is classified as information, never as a failure (vega#165).
var knownNoise = []noiseRule{
	{"logs.noise.acpi-firmware", regexp.MustCompile(`ACPI BIOS Error|ACPI Error|AE_ALREADY_EXISTS|AE_AML_REGION_LIMIT|\[Firmware Bug\]`)},
	{"logs.noise.boot", regexp.MustCompile(`integrity: Problem loading X\.509 certificate|TDX not supported|tpm_read_log_acpi|KillMode=none`)},
}

var gdmShutdownPattern = regexp.MustCompile(`^gdm\.service: Failed with result 'exit-code'`)

type logGroup struct {
	count int
	text  string
}

func sortedGroups(groups map[string]*logGroup, limit int) []string {
	list := make([]*logGroup, 0, len(groups))
	for _, g := range groups {
		list = append(list, g)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].count != list[j].count {
			return list[i].count > list[j].count
		}
		return list[i].text < list[j].text
	})
	var lines []string
	for i, g := range list {
		if i == limit {
			lines = append(lines, fmt.Sprintf("... +%d", len(list)-limit))
			break
		}
		lines = append(lines, fmt.Sprintf("%5d  %s", g.count, g.text))
	}
	return lines
}

func countGroups(groups map[string]*logGroup) int {
	total := 0
	for _, g := range groups {
		total += g.count
	}
	return total
}

// logClassification is the analysis of err+ journal entries across boots.
type logClassification struct {
	perBoot map[string]int
	noise   map[string]map[string]*logGroup
	errors  map[string]*logGroup
}

func classifyJournal(boots map[int][]journalEntry) logClassification {
	result := logClassification{perBoot: map[string]int{}, noise: map[string]map[string]*logGroup{}, errors: map[string]*logGroup{}}
	add := func(groups map[string]*logGroup, key string) {
		if g, ok := groups[key]; ok {
			g.count++
		} else {
			groups[key] = &logGroup{count: 1, text: key}
		}
	}
	for boot, entries := range boots {
		result.perBoot[strconv.Itoa(boot)] = len(entries)
		for _, entry := range entries {
			message := journalString(entry.Message)
			ident := journalString(entry.Identifier)
			if message == "" {
				continue
			}
			key := normalizeMessage(ident + ": " + message)
			code := ""
			for _, rule := range knownNoise {
				if rule.pattern.MatchString(message) {
					code = rule.code
					break
				}
			}
			// gdm stops with an error when AccountsService is already gone
			// during shutdown; only previous boots are known to have ended.
			if code == "" && boot < 0 && gdmShutdownPattern.MatchString(message) {
				code = "logs.noise.gdm-shutdown"
			}
			if code != "" {
				if result.noise[code] == nil {
					result.noise[code] = map[string]*logGroup{}
				}
				add(result.noise[code], key)
				continue
			}
			add(result.errors, key)
		}
	}
	return result
}

const coredumpMessageID = "fc2e22bc6ee647b6b90729ab34a250b1"

// scopeSuffix drops the per-launch part of a scope name: the PID in
// app-gnome-vega-7012.scope and the UUID in vte-spawn-<uuid>.scope.
var scopeSuffix = regexp.MustCompile(`-(?:[0-9]+|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.scope$`)

// coredumpGroups counts crashes per executable and per app scope, so the
// same binary crashing inside different apps stays distinguishable.
func coredumpGroups(entries []journalEntry) map[string]*logGroup {
	groups := map[string]*logGroup{}
	for _, entry := range entries {
		if journalString(entry.MessageID) != coredumpMessageID {
			continue
		}
		exe := journalString(entry.Exe)
		if exe == "" {
			exe = journalString(entry.Comm)
		}
		unit := journalString(entry.UserUnit)
		if unit == "" {
			unit = journalString(entry.CoreUnit)
		}
		unit = scopeSuffix.ReplaceAllString(unit, "")
		key := exe
		if unit != "" {
			key += " (" + unit + ")"
		}
		if signal := journalString(entry.Signal); signal != "" {
			key += " " + signal
		}
		if g, ok := groups[key]; ok {
			g.count++
		} else {
			groups[key] = &logGroup{count: 1, text: key}
		}
	}
	return groups
}

type failedUnit struct {
	Unit        string `json:"unit"`
	Description string `json:"description"`
}

func parseFailedUnits(out []byte) []failedUnit {
	var units []failedUnit
	_ = json.Unmarshal(out, &units)
	return units
}

// sinceSeen is the journalctl --since argument for "seen until": whole
// seconds rounded up, so nothing the user marked as seen comes back.
func sinceSeen(seen time.Time) string {
	seconds := seen.Unix()
	if seen.Nanosecond() > 0 {
		seconds++
	}
	return "--since=@" + strconv.FormatInt(seconds, 10)
}

// changedAfter reports whether a failed unit entered its state after the
// "seen" cutoff. A unit whose time cannot be read still counts.
func changedAfter(ctx context.Context, env *Env, unit string, seen time.Time) bool {
	out, _, err := env.Run(ctx, "systemctl", "show", "--timestamp=unix", "-p", "StateChangeTimestamp", "--value", unit)
	if err != nil {
		return true
	}
	value := strings.TrimPrefix(strings.TrimSpace(string(out)), "@")
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return true
	}
	return seconds >= seen.Unix()
}

func runLogs(ctx context.Context, env *Env, progress Progress) StageResult {
	start := time.Now()
	result := StageResult{ID: "logs"}
	seen := env.LogsSeenUntil
	progress(5, env.t("diagnostics.progress.logs.boots"))
	out, _, err := env.Run(ctx, "journalctl", "--list-boots", "-o", "json", "--no-pager")
	if err != nil {
		return failedToRun(env, result, "journalctl", err, start)
	}
	var boots []bootInfo
	_ = json.Unmarshal(out, &boots)
	sort.Slice(boots, func(i, j int) bool { return boots[i].Index > boots[j].Index })
	if len(boots) > env.BootsBack {
		boots = boots[:env.BootsBack]
	}
	entries := map[int][]journalEntry{}
	for i, boot := range boots {
		progress(uint32(10+60*i/max(1, len(boots))), env.t("diagnostics.progress.logs.journal", "boot", strconv.Itoa(boot.Index)))
		args := []string{"-b", strconv.Itoa(boot.Index), "-p", "err", "-o", "json", "--no-pager", "-q"}
		if !seen.IsZero() {
			args = append(args, sinceSeen(seen))
		}
		out, _, err := env.Run(ctx, "journalctl", args...)
		if err != nil {
			return failedToRun(env, result, "journalctl", err, start)
		}
		entries[boot.Index] = parseJournal(out)
	}
	classified := classifyJournal(entries)

	progress(75, env.t("diagnostics.progress.logs.coredumps"))
	since := "--since=-7d"
	if !seen.IsZero() && seen.After(start.Add(-7*24*time.Hour)) {
		since = sinceSeen(seen)
	}
	out, _, err = env.Run(ctx, "journalctl", "MESSAGE_ID="+coredumpMessageID, since, "-o", "json", "--no-pager", "-q")
	if err != nil {
		return failedToRun(env, result, "journalctl", err, start)
	}
	coredumps := coredumpGroups(parseJournal(out))

	progress(90, env.t("diagnostics.progress.logs.units"))
	out, _, err = env.Run(ctx, "systemctl", "--failed", "--output=json", "--no-pager")
	if err != nil {
		return failedToRun(env, result, "systemctl", err, start)
	}
	failed := parseFailedUnits(out)
	if !seen.IsZero() {
		kept := failed[:0]
		for _, unit := range failed {
			if changedAfter(ctx, env, unit.Unit, seen) {
				kept = append(kept, unit)
			}
		}
		failed = kept
	}

	var findings []Finding
	if n := countGroups(coredumps); n > 0 {
		findings = append(findings, env.finding(SeverityAttention, "logs.coredumps", strings.Join(sortedGroups(coredumps, 20), "\n"), "count", strconv.Itoa(n)))
	}
	if len(failed) > 0 {
		var lines []string
		for _, unit := range failed {
			lines = append(lines, unit.Unit+"  "+unit.Description)
		}
		units := env.finding(SeverityAttention, "logs.failed-units", strings.Join(lines, "\n"), "count", strconv.Itoa(len(failed)))
		for _, unit := range failed {
			if validUnitName(unit.Unit) {
				units.RemedyArgs = append(units.RemedyArgs, unit.Unit)
			}
		}
		if len(units.RemedyArgs) > 0 {
			units.Remedy = "restart-units"
		}
		findings = append(findings, units)
	}
	if n := countGroups(classified.errors); n > 0 {
		findings = append(findings, env.finding(Info, "logs.errors", strings.Join(sortedGroups(classified.errors, 30), "\n"), "count", strconv.Itoa(n), "unique", strconv.Itoa(len(classified.errors))))
	}
	noiseTotal := 0
	for _, code := range []string{"logs.noise.acpi-firmware", "logs.noise.boot", "logs.noise.gdm-shutdown"} {
		if groups := classified.noise[code]; len(groups) > 0 {
			n := countGroups(groups)
			noiseTotal += n
			findings = append(findings, env.finding(Info, code, strings.Join(sortedGroups(groups, 10), "\n"), "count", strconv.Itoa(n)))
		}
	}
	result.Findings = findings
	result.Metrics = map[string]any{
		// What this run examined; "Já vi" (MarkLogsSeen) uses it as the cutoff.
		"examinedUntil": start.UnixMicro(),
		"errorsPerBoot": classified.perBoot,
		"knownNoise":    noiseTotal,
		"coredumps":     countGroups(coredumps),
		"failedUnits":   len(failed),
	}
	if !seen.IsZero() {
		result.Metrics["seenUntil"] = seen.Format(time.RFC3339)
	}
	result.Verdict = verdictFromFindings(findings)
	result.Summary = env.t("diagnostics.summary.logs",
		"errors", strconv.Itoa(countGroups(classified.errors)),
		"boots", strconv.Itoa(len(boots)),
		"noise", strconv.Itoa(noiseTotal),
		"coredumps", strconv.Itoa(countGroups(coredumps)),
		"units", strconv.Itoa(len(failed)))
	result.DurationSeconds = int(time.Since(start).Seconds())
	return result
}
