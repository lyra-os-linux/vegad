package distro

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

// pacmanBackend drives Arch's Pacman as the PackageBackend. This is a
// pragmatic first cut shelling out to the pacman(8) CLI; PROMPT-VEGA.md
// §2.1 calls for libalpm directly, which a later pass can swap in behind
// this same interface.
type pacmanBackend struct{}

func newPacmanBackend() *pacmanBackend { return &pacmanBackend{} }

func (p *pacmanBackend) Name() string { return "Pacman" }

// pacmanResultLine matches the first line of each `pacman -Ss` hit, e.g.:
//
//	extra/firefox 152.0.4-1 [instalado]
//	extra/firefox-adblock-plus 4.41.0-1 (firefox-addons)
var pacmanResultLine = regexp.MustCompile(`^(\S+)/(\S+)\s+(\S+)`)

// Search shells out to `pacman -Ss`, reading whatever is already in the
// local sync databases — it does not run `-Sy` and therefore never touches
// the network or mutates system state, so it needs no privilege.
func (p *pacmanBackend) Search(query string) ([]PackageRef, error) {
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
		pending.Icon = FindPackageIcon(m[2])
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

// ListInstalled returns every locally installed Pacman package. Native repo
// packages are labelled "official"; foreign packages are labelled "aur"
// because that is how users usually encounter them in this UI.
func (p *pacmanBackend) ListInstalled() ([]PackageRef, error) {
	cmd := exec.Command("pacman", "-Qi")
	cmd.Env = commandEnvC()
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	foreign := map[string]bool{}
	foreignCmd := exec.Command("pacman", "-Qmq")
	if foreignOut, foreignErr := foreignCmd.Output(); foreignErr == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(foreignOut)))
		for scanner.Scan() {
			if name := strings.TrimSpace(scanner.Text()); name != "" {
				foreign[name] = true
			}
		}
	}

	var results []PackageRef
	for _, block := range strings.Split(string(out), "\n\n") {
		fields := parsePacmanInfoBlock([]byte(block))
		name := fields["Name"]
		if name == "" {
			continue
		}
		origin := "official"
		if foreign[name] {
			origin = "aur"
		}
		results = append(results, PackageRef{
			Origin:      origin,
			Id:          name,
			Name:        name,
			Description: fields["Description"],
			Installed:   true,
			Icon:        FindPackageIcon(name),
		})
	}
	return results, nil
}

// pacmanLockPath is a var, not const, so tests can point at a throwaway
// file instead of the real pacman lock and drive the retry loop
// deterministically.
var pacmanLockPath = "/var/lib/pacman/db.lck"
var pacmanLockRetryDelay = 2 * time.Second

const pacmanLockMaxAttempts = 5

// SyncDatabase runs `pacman -Sy`, refreshing the local sync databases from
// the configured repos. Unlike Search/ListUpdates this does touch the
// network and needs root — only called by the periodic update-check job,
// which already runs as root via its own systemd unit.
//
// The periodic check job races any other pacman/AUR-helper transaction the
// user might be running concurrently — pacman's own db lock file is the
// language-independent signal for that (unlike its error text, which is
// localized and unparseable). Rather than let that race fail the whole
// systemd unit, back off a few times and, if the lock is still held after
// pacmanLockMaxAttempts, skip this cycle quietly; the timer tries again in
// OnUnitActiveSec anyway.
func (p *pacmanBackend) SyncDatabase() error {
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

// ListUpdates reports pending updates among already-installed packages,
// based on whatever is in the local sync databases (no `-Sy`, so no network
// access and no privilege needed). Callers that need fresh results (e.g.
// the periodic check job) must SyncDatabase first.
func (p *pacmanBackend) ListUpdates() ([]PackageRef, error) {
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
			Icon:        FindPackageIcon(m[1]),
		})
	}
	return results, scanner.Err()
}

// FindPackageIcon looks up a package's icon in the standard FHS icon
// theme/pixmap paths — shared with the (distro-independent) Flatpak lookup
// as a final fallback.
func FindPackageIcon(id string) string {
	candidates := []string{
		filepath.Join("/usr/share/pixmaps", id+".png"),
		filepath.Join("/usr/share/pixmaps", id+".svg"),
		filepath.Join("/usr/share/icons/hicolor/scalable/apps", id+".svg"),
		filepath.Join("/usr/share/icons/hicolor/256x256/apps", id+".png"),
		filepath.Join("/usr/share/icons/hicolor/128x128/apps", id+".png"),
		filepath.Join("/usr/share/icons/hicolor/64x64/apps", id+".png"),
		filepath.Join("/usr/share/icons/hicolor/48x48/apps", id+".png"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
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

// SplitPackageList splits a space-separated pacman-style list field
// ("Depends On", "Licenses", ...) into its entries, treating pacman's own
// "None" as empty. Shared with flatpak.go's license parsing since both
// formats use the same convention.
func SplitPackageList(value string) []string {
	if value == "" || value == "None" {
		return nil
	}
	return strings.Fields(value)
}

// GetDetails runs `pacman -Si` for the sync-database view of a package
// (works whether or not it's installed, no `-Sy` — same no-network/no-privilege
// reasoning as ListUpdates) and, if the package is installed, layers
// `pacman -Qi` on top for the installed version/size, which -Si never
// reports.
func (p *pacmanBackend) GetDetails(id string) (PackageDetails, error) {
	details := PackageDetails{Origin: "official", Id: id}

	cmd := exec.Command("pacman", "-Si", "--", id)
	cmd.Env = commandEnvC()
	out, err := cmd.Output()
	if err != nil {
		return details, fmt.Errorf("pacman -Si %s: %w", id, err)
	}
	fields := parsePacmanInfoBlock(out)
	details.Name = fields["Name"]
	details.Description = fields["Description"]
	details.URL = fields["URL"]
	details.Licenses = SplitPackageList(fields["Licenses"])
	details.Dependencies = SplitPackageList(fields["Depends On"])
	details.AvailableVersion = fields["Version"]
	details.DownloadSize = fields["Download Size"]

	installed, err := pacmanInstalledSet()
	if err == nil && installed[id] {
		details.Installed = true
		icmd := exec.Command("pacman", "-Qi", "--", id)
		icmd.Env = commandEnvC()
		if iout, ierr := icmd.Output(); ierr == nil {
			ifields := parsePacmanInfoBlock(iout)
			details.InstalledVersion = ifields["Version"]
			details.InstalledSize = ifields["Installed Size"]
		}
	}

	return details, nil
}

// Install runs `pacman -S` for a single package, reporting coarse progress
// as pacman announces its stages on stdout.
func (p *pacmanBackend) Install(pkg string, report ProgressFunc) error {
	return runPacmanTransaction([]string{"-S", "--noconfirm", "--", pkg}, report)
}

// Remove runs `pacman -R` for a single package.
func (p *pacmanBackend) Remove(pkg string, report ProgressFunc) error {
	return runPacmanTransaction([]string{"-R", "--noconfirm", "--", pkg}, report)
}

// UpdateAll runs a full sync + upgrade (`pacman -Syu`).
func (p *pacmanBackend) UpdateAll(report ProgressFunc) error {
	return runPacmanTransaction([]string{"-Syu", "--noconfirm"}, report)
}

// ClearCache runs `pacman -Scc`, removing all cached package files.
func (p *pacmanBackend) ClearCache(report ProgressFunc) error {
	return runPacmanTransaction([]string{"-Scc", "--noconfirm"}, report)
}

// OptimizeMirrors ranks pacman mirrors by download speed and rewrites
// /etc/pacman.d/mirrorlist — reflector is an optdepend (packaging/vegad/
// PKGBUILD), same fallback pattern as yay/paru for AUR.
func (p *pacmanBackend) OptimizeMirrors(report ProgressFunc) error {
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
func runPacmanTransaction(args []string, report ProgressFunc) error {
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

// ListRepos parses /etc/pacman.conf for `[section]` headers, skipping the
// special [options] section.
func (p *pacmanBackend) ListRepos() ([]string, error) {
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

func (p *pacmanBackend) SetRepoEnabled(repo string, enabled bool) error {
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
