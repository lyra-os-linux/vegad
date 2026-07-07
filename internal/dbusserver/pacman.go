package dbusserver

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
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
		pending = &PackageRef{Origin: "official", Id: m[2], Name: m[2], Installed: installed[m[2]]}
	}
	if pending != nil {
		results = append(results, *pending)
	}
	return results, nil
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

// pacmanUpdateLine matches a `pacman -Qu` line, e.g. "firefox 151.0-1 -> 152.0.4-1".
var pacmanUpdateLine = regexp.MustCompile(`^(\S+)\s+(\S+)\s+->\s+(\S+)`)

// listPacmanUpdates reports pending updates among already-installed
// packages, based on whatever is in the local sync databases (no `-Sy`, so
// no network access and no privilege needed).
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
