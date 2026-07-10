package dbusserver

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// progressFunc reports coarse (stage-based, not byte-accurate) progress for
// a running transaction — pacman/flatpak's own textual output doesn't carry
// a reliable machine-readable percentage, so callers get milestones instead.
type progressFunc func(percent uint32, message string)

// pacmanResultLine matches the first line of each `pacman -Ss` hit, e.g.:
//
//	extra/firefox 152.0.4-1 [instalado]
//	extra/firefox-adblock-plus 4.41.0-1 (firefox-addons)
var pacmanResultLine = regexp.MustCompile(`^(\S+)/(\S+)\s+(\S+)`)

// searchPacman shells out to `pacman -Ss`, reading whatever is already in
// the local sync databases — it does not run `-Sy` and therefore never
// touches the network or mutates system state, so it needs no privilege.
// This is a pragmatic first cut; PROMPT-VEGA.md §2.1 calls for libalpm
// directly, which a later pass can swap in behind this same function
// signature.
func searchPacman(query string) ([]PackageRef, error) {
	installed, err := pacmanInstalledSet()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("pacman", "-Ss", "--", query)
	out, err := cmd.Output()
	if err != nil {
		// pacman -Ss exits 1 with empty output when nothing matches —
		// that's not a real error condition for a search.
		if _, ok := err.(*exec.ExitError); ok && len(out) == 0 {
			return nil, nil
		}
		return nil, err
	}

	return parseSearchOutput(out, "official", installed), nil
}

// parseSearchOutput parses the shared "repo/name version [...]" + indented
// description format used by both `pacman -Ss` and `yay`/`paru -Ssa` — the
// AUR helpers wrap the same presentation convention, just with "aur/" as the
// repo prefix, so one parser covers both (see aur.go's searchAur).
func parseSearchOutput(out []byte, origin string, installed map[string]bool) []PackageRef {
	var results []PackageRef
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	var pending *PackageRef
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if pending != nil {
				pending.Description = strings.TrimSpace(line)
				results = append(results, *pending)
				pending = nil
			}
			continue
		}
		m := pacmanResultLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pending = &PackageRef{Origin: origin, Id: m[2], Name: m[2], Installed: installed[m[2]]}
	}
	if pending != nil {
		results = append(results, *pending)
	}
	return results
}

// pacmanInstalledSet returns the set of currently installed package names
// (`pacman -Qq`), used to flag search results as installed without a
// per-package lookup.
func pacmanInstalledSet() (map[string]bool, error) {
	cmd := exec.Command("pacman", "-Qq")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		if name := strings.TrimSpace(scanner.Text()); name != "" {
			set[name] = true
		}
	}
	return set, scanner.Err()
}

// syncPacmanDb runs `pacman -Sy`, refreshing the local sync databases from
// the configured repos. Unlike searchPacman/listPacmanUpdates this does
// touch the network and needs root — called by RunUpdateCheckJob, which
// already runs as root via its own systemd unit, so nothing is lost by
// syncing before checking.
//
// The periodic check job races any other pacman/AUR-helper transaction the
// user might be running concurrently — pacman's own db lock file is the
// language-independent signal for that (unlike its error text, which is
// localized and unparseable). Rather than let that race fail the whole
// systemd unit, back off a few times and, if the lock is still held after
// pacmanLockMaxAttempts, skip this cycle quietly; the timer tries again in
// OnUnitActiveSec anyway.
// var, not const, so tests can point at a throwaway file instead of the
// real pacman lock and drive the retry loop deterministically.
var pacmanLockPath = "/var/lib/pacman/db.lck"
var pacmanLockRetryDelay = 2 * time.Second

const pacmanLockMaxAttempts = 5

func syncPacmanDb() error {
	for attempt := 1; attempt <= pacmanLockMaxAttempts; attempt++ {
		if _, err := os.Stat(pacmanLockPath); err == nil {
			if attempt == pacmanLockMaxAttempts {
				log.Printf("vegad: pacman -Sy adiado — banco de pacotes ocupado por outra transação")
				return nil
			}
			time.Sleep(pacmanLockRetryDelay)
			continue
		}

		cmd := exec.Command("pacman", "-Sy")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("pacman -Sy: %w — %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return nil
}

// pacmanUpdateLine matches a `pacman -Qu` line, e.g. "firefox 151.0-1 -> 152.0.4-1".
var pacmanUpdateLine = regexp.MustCompile(`^(\S+)\s+(\S+)\s+->\s+(\S+)`)

// listPacmanUpdates reports pending updates among already-installed
// packages, based on whatever is in the local sync databases (no `-Sy`, so
// no network access and no privilege needed). Callers that need fresh
// results (e.g. the periodic check job) must sync first — see syncPacmanDb.
func listPacmanUpdates() ([]PackageRef, error) {
	cmd := exec.Command("pacman", "-Qu")
	out, err := cmd.Output()
	if err != nil {
		// exit 1 with empty output means "nothing to update", not a failure.
		if _, ok := err.(*exec.ExitError); ok && len(out) == 0 {
			return nil, nil
		}
		return nil, err
	}

	var results []PackageRef
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		m := pacmanUpdateLine.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		results = append(results, PackageRef{
			Origin:      "official",
			Id:          m[1],
			Name:        m[1],
			Description: fmt.Sprintf("%s → %s", m[2], m[3]),
			Installed:   true,
		})
	}
	return results, scanner.Err()
}

// pacmanCommandEnv forces deterministic English field labels ("Version",
// "Depends On", ...) out of pacman/AUR-helper info commands — under a
// non-English system locale pacman prints those localized (e.g. "Versão"),
// which parsePacmanInfoBlock can't recognize. Same fix as snapper.go's
// LC_ALL=C for --csvout headers.
func pacmanCommandEnv() []string {
	return append(os.Environ(), "LC_ALL=C")
}

// parsePacmanInfoBlock parses the "Key : Value" layout shared by `pacman
// -Si`/`-Qi` and `yay`/`paru -Si` under LC_ALL=C. Wrapped continuation
// lines (e.g. a long "Depends On" list) are indented and lack the " : "
// separator, so they get appended to the previous key's value.
func parsePacmanInfoBlock(out []byte) map[string]string {
	fields := map[string]string{}
	lastKey := ""
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			lastKey = ""
			continue
		}
		if !strings.HasPrefix(line, " ") {
			if idx := strings.Index(line, " : "); idx > 0 {
				key := strings.TrimSpace(line[:idx])
				fields[key] = strings.TrimSpace(line[idx+3:])
				lastKey = key
				continue
			}
		}
		if lastKey != "" {
			fields[lastKey] = strings.TrimSpace(fields[lastKey] + " " + strings.TrimSpace(line))
		}
	}
	return fields
}

// splitPacmanList splits a space-separated pacman list field ("Depends On",
// "Licenses", ...) into its entries, treating pacman's own "None" as empty.
func splitPacmanList(value string) []string {
	if value == "" || value == "None" {
		return nil
	}
	return strings.Fields(value)
}

// fetchPacmanDetails runs `pacman -Si` for the sync-database view of a
// package (works whether or not it's installed, no `-Sy` — same
// no-network/no-privilege reasoning as listPacmanUpdates) and, if the
// package is installed, layers `pacman -Qi` on top for the installed
// version/size, which -Si never reports.
func fetchPacmanDetails(id string) (PackageDetails, error) {
	details := PackageDetails{Origin: "official", Id: id}

	cmd := exec.Command("pacman", "-Si", "--", id)
	cmd.Env = pacmanCommandEnv()
	out, err := cmd.Output()
	if err != nil {
		return details, fmt.Errorf("pacman -Si %s: %w", id, err)
	}
	fields := parsePacmanInfoBlock(out)
	details.Name = fields["Name"]
	details.Description = fields["Description"]
	details.URL = fields["URL"]
	details.Licenses = splitPacmanList(fields["Licenses"])
	details.Dependencies = splitPacmanList(fields["Depends On"])
	details.AvailableVersion = fields["Version"]
	details.DownloadSize = fields["Download Size"]

	installed, err := pacmanInstalledSet()
	if err == nil && installed[id] {
		details.Installed = true
		icmd := exec.Command("pacman", "-Qi", "--", id)
		icmd.Env = pacmanCommandEnv()
		if iout, ierr := icmd.Output(); ierr == nil {
			ifields := parsePacmanInfoBlock(iout)
			details.InstalledVersion = ifields["Version"]
			details.InstalledSize = ifields["Installed Size"]
		}
	}

	return details, nil
}

// installPacman runs `pacman -S` for a single package, reporting coarse
// progress as pacman announces its stages on stdout.
func installPacman(pkg string, report progressFunc) error {
	return runPacmanTransaction([]string{"-S", "--noconfirm", "--", pkg}, report)
}

// removePacman runs `pacman -R` for a single package.
func removePacman(pkg string, report progressFunc) error {
	return runPacmanTransaction([]string{"-R", "--noconfirm", "--", pkg}, report)
}

// updateAllPacman runs a full sync + upgrade (`pacman -Syu`) — unlike
// search/list, this does touch the network and mutate system state, which
// is exactly what the user asked for by clicking "Atualizar tudo"
// (PROMPT-VEGA.md §3.1).
func updateAllPacman(report progressFunc) error {
	return runPacmanTransaction([]string{"-Syu", "--noconfirm"}, report)
}

// clearPacmanCache runs `pacman -Scc`, removing all cached package files.
func clearPacmanCache(report progressFunc) error {
	return runPacmanTransaction([]string{"-Scc", "--noconfirm"}, report)
}

// optimizeMirrors ranks pacman mirrors by download speed and rewrites
// /etc/pacman.d/mirrorlist — reflector is an optdepend (packaging/vegad/
// PKGBUILD), same fallback pattern as yay/paru for AUR.
func optimizeMirrors(report progressFunc) error {
	if !commandAvailable("reflector") {
		return fmt.Errorf("reflector não está instalado — instale o pacote 'reflector' para otimizar mirrors")
	}
	return runStreamingCommand(
		"reflector",
		[]string{"--latest", "20", "--sort", "rate", "--save", "/etc/pacman.d/mirrorlist"},
		report, "Testando velocidade dos mirrors...", "Lista de mirrors atualizada",
	)
}

var (
	pacmanStageInstalling = regexp.MustCompile(`^\((\d+)/(\d+)\) (installing|upgrading|removing) `)
	pacmanStageDownload   = regexp.MustCompile(`^:: Retrieving packages`)
)

// runPacmanTransaction runs pacman with the given args, reporting progress
// derived from its "(n/total) installing foo" style lines. Text-based
// progress is a pragmatic stand-in for the byte-accurate percentages a
// direct libalpm binding would give (PROMPT-VEGA.md §2.1 calls for libalpm
// eventually).
func runPacmanTransaction(args []string, report progressFunc) error {
	report(0, "Iniciando...")

	cmd := exec.Command("pacman", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdout)
	var lastLines []string
	for scanner.Scan() {
		line := scanner.Text()
		lastLines = append(lastLines, line)
		if len(lastLines) > 20 {
			lastLines = lastLines[1:]
		}

		switch {
		case pacmanStageDownload.MatchString(line):
			report(20, "Baixando pacotes...")
		case pacmanStageInstalling.MatchString(line):
			m := pacmanStageInstalling.FindStringSubmatch(line)
			var n, total int
			fmt.Sscanf(m[1], "%d", &n)
			fmt.Sscanf(m[2], "%d", &total)
			percent := uint32(40)
			if total > 0 {
				percent = uint32(40 + (float64(n) / float64(total) * 55))
			}
			report(percent, line)
		}
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("pacman: %w — %s", err, strings.Join(lastLines, " | "))
	}
	report(100, "Concluído")
	return nil
}

// listPacmanRepos parses /etc/pacman.conf for `[section]` headers, skipping
// the special [options] section.
func listPacmanRepos() ([]string, error) {
	f, err := os.Open("/etc/pacman.conf")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var repos []string
	sectionRe := regexp.MustCompile(`^\[([^\]]+)\]$`)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := sectionRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[1] == "options" {
			continue
		}
		repos = append(repos, m[1])
	}
	return repos, scanner.Err()
}

func setPacmanRepoEnabled(repo string, enabled bool) error {
	data, err := os.ReadFile("/etc/pacman.conf")
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	inTarget := false
	found := false

	sectionRe := regexp.MustCompile(`^\s*#?\s*\[([^\]]+)\]\s*$`)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if m := sectionRe.FindStringSubmatch(trimmed); m != nil {
			if inTarget {
				inTarget = false
			}
			isTarget := m[1] == repo
			if isTarget {
				found = true
				inTarget = true
				if enabled {
					out = append(out, fmt.Sprintf("[%s]", repo))
				} else {
					out = append(out, fmt.Sprintf("# [%s]", repo))
				}
				continue
			}
		}

		if inTarget {
			if enabled {
				out = append(out, strings.TrimPrefix(strings.TrimPrefix(line, "# "), "#"))
			} else if strings.TrimSpace(line) == "" {
				out = append(out, line)
			} else if strings.HasPrefix(strings.TrimSpace(line), "#") {
				out = append(out, line)
			} else {
				out = append(out, "# "+line)
			}
			continue
		}
		out = append(out, line)
	}

	if !found {
		return fmt.Errorf("repositório %q não encontrado em pacman.conf", repo)
	}

	tmp := filepath.Join(filepath.Dir("/etc/pacman.conf"), ".pacman.conf.vega")
	if err := os.WriteFile(tmp, []byte(strings.Join(out, "\n")), 0o644); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := os.Rename(tmp, "/etc/pacman.conf"); err != nil {
		return err
	}
	return nil
}
