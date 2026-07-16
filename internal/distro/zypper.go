package distro

import (
	"bufio"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// zypperBackend drives openSUSE Leap's Zypper as the PackageBackend, the
// same pragmatic CLI-shelling approach pacmanBackend takes for Arch.
type zypperBackend struct{}

func newZypperBackend() *zypperBackend { return &zypperBackend{} }

func (z *zypperBackend) Name() string { return "Zypper" }

// zypperInstalledSet returns the set of currently installed package names,
// via rpm rather than zypper itself — much cheaper than parsing a table for
// a plain membership check.
func zypperInstalledSet() (map[string]bool, error) {
	out, err := runCommandOutput("rpm", "-qa", "--qf", "%{NAME}\n")
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, name := range strings.Split(out, "\n") {
		if name = strings.TrimSpace(name); name != "" {
			set[name] = true
		}
	}
	return set, nil
}

// splitZypperTableLine splits one row of zypper's "|"-delimited table
// output, trimming the padding zypper adds around each column.
func splitZypperTableLine(line string) []string {
	parts := strings.Split(line, "|")
	fields := make([]string, len(parts))
	for i, p := range parts {
		fields[i] = strings.TrimSpace(p)
	}
	return fields
}

// isZypperTableRule reports whether line is one of the "--+----+--" rules
// zypper draws around its table headers, rather than a data row.
func isZypperTableRule(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	return strings.Trim(trimmed, "-+") == ""
}

// Search shells out to `zypper search`, which (like pacman -Ss) only reads
// the already-refreshed local metadata — no network access, no privilege.
func (z *zypperBackend) Search(query string) ([]PackageRef, error) {
	installed, err := zypperInstalledSet()
	if err != nil {
		return nil, err
	}

	out, err := runCommandOutput("zypper", "--non-interactive", "search", "--", query)
	if err != nil {
		// zypper exits non-zero with no results when nothing matches —
		// not a real error condition for a search.
		if _, ok := err.(*exec.ExitError); ok {
			return nil, nil
		}
		return nil, err
	}

	var results []PackageRef
	seenHeader := false
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if isZypperTableRule(line) {
			seenHeader = true
			continue
		}
		if !seenHeader || !strings.Contains(line, "|") {
			continue
		}
		fields := splitZypperTableLine(line)
		if len(fields) < 3 {
			continue
		}
		name := fields[1]
		if name == "" || name == "Name" {
			continue
		}
		results = append(results, PackageRef{
			Origin:      "official",
			Id:          name,
			Name:        name,
			Description: fields[2],
			Installed:   installed[name],
			Icon:        FindPackageIcon(name),
		})
	}
	return results, scanner.Err()
}

// ListInstalled reports every RPM-installed package via `rpm -qa`, which is
// far cheaper than asking zypper to cross-reference its repo metadata for a
// plain "what's on disk" listing.
func (z *zypperBackend) ListInstalled() ([]PackageRef, error) {
	out, err := runCommandOutput("rpm", "-qa", "--qf", "%{NAME}\t%{SUMMARY}\n")
	if err != nil {
		return nil, err
	}

	var results []PackageRef
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), "\t", 2)
		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}
		desc := ""
		if len(parts) == 2 {
			desc = strings.TrimSpace(parts[1])
		}
		results = append(results, PackageRef{
			Origin:      "official",
			Id:          name,
			Name:        name,
			Description: desc,
			Installed:   true,
			Icon:        FindPackageIcon(name),
		})
	}
	return results, scanner.Err()
}

// SyncDatabase runs `zypper refresh`, refreshing repo metadata from the
// configured repos — touches the network and needs root, same restriction
// as pacmanBackend.SyncDatabase.
func (z *zypperBackend) SyncDatabase() error {
	out, err := runCommandOutput("zypper", "--non-interactive", "refresh")
	if err != nil {
		return fmt.Errorf("zypper refresh: %w — %s", err, out)
	}
	return nil
}

// zypperParseUpdates runs `zypper list-updates` (optionally with extra args
// such as --all) and parses its "S | Repository | Name | Current Version |
// Available Version | Arch" table.
func zypperParseUpdates(extraArgs ...string) ([]PackageRef, error) {
	args := append([]string{"--non-interactive", "list-updates"}, extraArgs...)
	out, err := runCommandOutput("zypper", args...)
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return nil, nil
		}
		return nil, err
	}

	var results []PackageRef
	seenHeader := false
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if isZypperTableRule(line) {
			seenHeader = true
			continue
		}
		if !seenHeader || !strings.Contains(line, "|") {
			continue
		}
		fields := splitZypperTableLine(line)
		if len(fields) < 5 {
			continue
		}
		name := fields[2]
		if name == "" || name == "Name" {
			continue
		}
		results = append(results, PackageRef{
			Origin:      "official",
			Id:          name,
			Name:        name,
			Description: fmt.Sprintf("%s → %s", fields[3], fields[4]),
			Installed:   true,
			Icon:        FindPackageIcon(name),
		})
	}
	return results, scanner.Err()
}

// ListUpdates reports pending updates among installed packages from
// whatever is in the local repo metadata (no refresh, so no network access
// needed). Callers that need fresh results must SyncDatabase first.
//
// Plain `zypper list-updates` — like `zypper update` — silently drops
// updates that would require a vendor change (e.g. a proprietary driver
// package offered by both the distro's repo and the vendor's own repo).
// Those don't error out or show up anywhere; they just vanish, and `zypper
// update` reports "will not be installed" with no further explanation. This
// runs list-updates a second time with --all (which includes them) and
// flags whatever's missing from the plain run so the UI can tell the user
// they need manual resolution instead of quietly never appearing.
func (z *zypperBackend) ListUpdates() ([]PackageRef, error) {
	safe, err := zypperParseUpdates()
	if err != nil {
		return nil, err
	}
	all, err := zypperParseUpdates("--all")
	if err != nil {
		return nil, err
	}

	safeNames := make(map[string]bool, len(safe))
	for _, pkg := range safe {
		safeNames[pkg.Id] = true
	}

	results := safe
	for _, pkg := range all {
		if safeNames[pkg.Id] {
			continue
		}
		pkg.Description += " — requer troca de fornecedor, não coberto por \"Atualizar tudo\""
		results = append(results, pkg)
	}
	return results, nil
}

// parseZypperInfoBlock parses the "Key : Value" layout of `zypper info`'s
// output. Description continuation lines have no colon and are skipped
// rather than folded in, since the single-line Summary field already
// covers what pacmanBackend's "Description" is used for.
func parseZypperInfoBlock(out string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		fields[key] = strings.TrimSpace(line[idx+1:])
	}
	return fields
}

func humanizeBytes(raw string) string {
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", n, units[i])
}

// GetDetails layers `rpm -q` (for the installed view — license, URL, exact
// size, which `zypper info` either omits or only shows for pending updates)
// on top of `zypper info` (for the repo view — download size, sync-database
// version when not yet installed).
func (z *zypperBackend) GetDetails(id string) (PackageDetails, error) {
	details := PackageDetails{Origin: "official", Id: id}

	installed, err := zypperInstalledSet()
	if err != nil {
		return details, err
	}
	details.Installed = installed[id]

	if details.Installed {
		out, err := runCommandOutput("rpm", "-q", "--qf",
			"%{NAME}\t%{VERSION}-%{RELEASE}\t%{SUMMARY}\t%{LICENSE}\t%{URL}\t%{SIZE}\n", "--", id)
		if err != nil {
			return details, fmt.Errorf("rpm -q %s: %w", id, err)
		}
		fields := strings.SplitN(strings.TrimSpace(out), "\t", 6)
		if len(fields) == 6 {
			details.Name = fields[0]
			details.InstalledVersion = fields[1]
			details.AvailableVersion = fields[1]
			details.Description = fields[2]
			details.Licenses = []string{fields[3]}
			details.URL = fields[4]
			details.InstalledSize = humanizeBytes(fields[5])
		}
	}

	if out, err := runCommandOutput("zypper", "--non-interactive", "info", "--", id); err == nil {
		info := parseZypperInfoBlock(out)
		if details.Name == "" {
			details.Name = info["Name"]
			details.Description = info["Summary"]
		}
		if v := info["Version"]; v != "" {
			details.AvailableVersion = v
		}
		if size := info["Download Size"]; size != "" {
			details.DownloadSize = size
		}
	}

	return details, nil
}

func (z *zypperBackend) Install(pkg string, report ProgressFunc) error {
	return runStreamingCommand("zypper", []string{"--non-interactive", "install", "-y", "--", pkg}, report,
		"Iniciando instalação...", "Instalação concluída")
}

func (z *zypperBackend) Remove(pkg string, report ProgressFunc) error {
	return runStreamingCommand("zypper", []string{"--non-interactive", "remove", "-y", "--", pkg}, report,
		"Iniciando remoção...", "Remoção concluída")
}

// UpdateAll runs `zypper update`, upgrading already-installed packages
// (the Zypper analogue of `pacman -Syu`, not a full `dup` distribution
// upgrade).
func (z *zypperBackend) UpdateAll(report ProgressFunc) error {
	return runStreamingCommand("zypper", []string{"--non-interactive", "update", "-y"}, report,
		"Iniciando atualização...", "Atualização concluída")
}

func (z *zypperBackend) ClearCache(report ProgressFunc) error {
	return runStreamingCommand("zypper", []string{"clean", "--all"}, report,
		"Limpando cache...", "Cache limpo")
}

// OptimizeMirrors has no Zypper equivalent to expose: Leap's download
// redirector (download.opensuse.org) already picks the best mirror per
// request, unlike pacman's static mirrorlist that reflector re-ranks.
func (z *zypperBackend) OptimizeMirrors(report ProgressFunc) error {
	return ErrUnsupported
}

func (z *zypperBackend) ListRepos() ([]RepositoryRef, error) {
	out, err := runCommandOutput("zypper", "--non-interactive", "repos")
	if err != nil {
		return nil, fmt.Errorf("zypper repos: %w — %s", err, out)
	}

	var repos []RepositoryRef
	seenHeader := false
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if isZypperTableRule(line) {
			seenHeader = true
			continue
		}
		if !seenHeader || !strings.Contains(line, "|") {
			continue
		}
		fields := splitZypperTableLine(line)
		if len(fields) < 4 || fields[1] == "" {
			continue
		}
		repos = append(repos, RepositoryRef{
			Name:    fields[1],
			Enabled: strings.EqualFold(fields[3], "yes") || strings.EqualFold(fields[3], "sim"),
		})
	}
	return repos, scanner.Err()
}

// SetRepoEnabled uses zypper's own modifyrepo subcommand — unlike pacman,
// which needs pacman.conf rewritten by hand, zypper tracks repo state
// itself so there's no config file to munge.
func (z *zypperBackend) SetRepoEnabled(repo string, enabled bool) error {
	flag := "--disable"
	if enabled {
		flag = "--enable"
	}
	out, err := runCommandOutput("zypper", "--non-interactive", "modifyrepo", flag, "--", repo)
	if err != nil {
		return fmt.Errorf("zypper modifyrepo %s: %w — %s", repo, err, out)
	}
	return nil
}
