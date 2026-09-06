package distro

import (
	"bufio"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// zypperBackend drives openSUSE Leap's Zypper as the PackageBackend, the
// same pragmatic CLI-shelling approach pacmanBackend takes for Arch.
type zypperBackend struct {
	keyMu       sync.Mutex
	pendingKeys map[string]repoKeyApproval
}

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

func zypperSearchArgs(query string) []string {
	// Search must remain a read-only, fast operation over the existing local
	// metadata. Repository refresh belongs to the explicit refresh/update
	// flows; doing it here makes every Vega search depend on network and on
	// the availability of every configured mirror.
	return []string{"--non-interactive", "--no-refresh", "search", "--", query}
}

// Search shells out to `zypper search`, which (like pacman -Ss) only reads
// the already-refreshed local metadata — no network access, no privilege.
func (z *zypperBackend) Search(query string) ([]PackageRef, error) {
	installed, err := zypperInstalledSet()
	if err != nil {
		return nil, err
	}

	out, err := runCommandOutput("zypper", zypperSearchArgs(query)...)
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
			Repository:  fields[1],
		})
	}
	return results, scanner.Err()
}

// zypperRepoAlias pairs a configured repo's alias (needed for `zypper
// update --repo <alias>`) with its display Name (what `zypper list-updates`
// prints in its "Repository" column — confirmed empirically to be the Name
// field, not the alias, which can otherwise be a bare URL for repos added
// without an explicit alias).
type zypperRepoAlias struct {
	Alias string
	Name  string
}

// zypperRepoOrder parses `zypper repos` and returns every repo's
// alias+name pair in the same order zypper itself lists them (its own
// numbering) — the single source of truth for both display order (Updates
// tab, grouped by repo) and execution order (UpdateAll, repo by repo).
func zypperRepoOrder() ([]zypperRepoAlias, error) {
	out, err := runCommandOutput("zypper", "--non-interactive", "repos")
	if err != nil {
		return nil, fmt.Errorf("falha ao listar repositórios: %w — %s", err, out)
	}

	var repos []zypperRepoAlias
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
		if len(fields) < 3 || fields[1] == "" {
			continue
		}
		repos = append(repos, zypperRepoAlias{Alias: fields[1], Name: fields[2]})
	}
	return repos, scanner.Err()
}

// RepoUpdateGroup is one repository's slice of the pending "safe" updates
// (see zypperParseUpdates — excludes vendor-change updates, same set plain
// `zypper update` already limits itself to).
type RepoUpdateGroup struct {
	Alias       string
	DisplayName string
	Packages    []PackageRef
}

// zypperGroupedUpdates groups pending updates by source repository, in
// repo-numbering order (zypperRepoOrder), skipping repos with nothing
// pending. This is the single source of truth for both the Updates tab's
// display order and UpdateAll's execution order, so the two can never
// drift apart.
func zypperGroupedUpdates() ([]RepoUpdateGroup, error) {
	updates, err := zypperParseUpdates()
	if err != nil {
		return nil, err
	}
	repos, err := zypperRepoOrder()
	if err != nil {
		return nil, err
	}

	byRepoName := make(map[string][]PackageRef, len(updates))
	for _, pkg := range updates {
		byRepoName[pkg.Repository] = append(byRepoName[pkg.Repository], pkg)
	}

	var groups []RepoUpdateGroup
	seen := make(map[string]bool, len(repos))
	for _, repo := range repos {
		packages := byRepoName[repo.Name]
		if len(packages) == 0 {
			continue
		}
		seen[repo.Name] = true
		groups = append(groups, RepoUpdateGroup{Alias: repo.Alias, DisplayName: repo.Name, Packages: packages})
	}
	// Updates whose Repository doesn't match any known repo Name (shouldn't
	// normally happen, but repos can change between the two zypper calls)
	// still need to be reachable — surface them as their own group keyed by
	// the raw name, alias falling back to the same string. Sorted for
	// deterministic ordering since map iteration order isn't.
	var leftoverNames []string
	for name := range byRepoName {
		if !seen[name] {
			leftoverNames = append(leftoverNames, name)
		}
	}
	sort.Strings(leftoverNames)
	for _, name := range leftoverNames {
		groups = append(groups, RepoUpdateGroup{Alias: name, DisplayName: name, Packages: byRepoName[name]})
	}
	return groups, nil
}

// ListUpdates reports pending updates among installed packages from
// whatever is in the local repo metadata (no refresh, so no network access
// needed). Callers that need fresh results must SyncDatabase first.
//
// Plain `zypper list-updates` returns the same installable set as `zypper
// update`: packages that would require a vendor change or otherwise appear
// under "will not be installed" are omitted. Keep that behavior here so
// clients never advertise packages that UpdateAll will leave untouched.
//
// The installable set is grouped and ordered by source repository
// (zypperGroupedUpdates), the same grouping/order UpdateAll executes in —
// so the Updates tab's list and the actual repo-by-repo update run always
// agree on ordering.
func (z *zypperBackend) ListUpdates() ([]PackageRef, error) {
	groups, err := zypperGroupedUpdates()
	if err != nil {
		return nil, err
	}

	var results []PackageRef
	for _, group := range groups {
		results = append(results, group.Packages...)
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

func (z *zypperBackend) Install(pkg string, report ProgressFunc, pkgReport PackageProgressFunc) error {
	return runZypperTransactionXML([]string{"--non-interactive", "install", "-y", "--", pkg}, report, pkgReport,
		"Iniciando instalação...", "Instalação concluída")
}

func (z *zypperBackend) Remove(pkg string, report ProgressFunc, pkgReport PackageProgressFunc) error {
	return runZypperTransactionXML([]string{"--non-interactive", "remove", "-y", "--", pkg}, report, pkgReport,
		"Iniciando remoção...", "Remoção concluída")
}

// UpdateAll upgrades already-installed packages one repository at a time
// (in zypperGroupedUpdates' repo-numbering order), rather than a single
// `zypper update` across every repo at once — so the user can watch each
// repo's packages finish before the next one starts, matching the order
// already shown in the Updates tab. A failure in one repo doesn't stop the
// rest: remaining repos still run, and every failure is collected into one
// final error.
func (z *zypperBackend) UpdateAll(report ProgressFunc, pkgReport PackageProgressFunc) error {
	groups, err := zypperGroupedUpdates()
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		report(100, "Nenhuma atualização pendente")
		return nil
	}

	total := 0
	for _, group := range groups {
		total += len(group.Packages)
	}

	var completed int
	var failures []string
	for _, group := range groups {
		repoSize := len(group.Packages)
		wrapped := func(percent uint32, message string) {
			overall := (completed*100 + int(percent)*repoSize) / total
			report(uint32(overall), message)
		}
		err := runZypperTransactionXML(
			[]string{"--non-interactive", "update", "-y", "--repo", group.Alias},
			wrapped, pkgReport,
			fmt.Sprintf("Atualizando %s...", group.DisplayName), "Atualização concluída")
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", group.DisplayName, err))
		}
		completed += repoSize
	}

	if len(failures) > 0 {
		return fmt.Errorf("não foi possível atualizar pacotes de %d repositório(s): %s", len(failures), strings.Join(failures, "; "))
	}
	report(100, "Atualização concluída")
	return nil
}

// UpdatePackage updates a single package via `zypper update`, restricted to
// that one package name — zypper's own dependency resolver still pulls in
// whatever dependency bump it needs, the same as it would for the matching
// package inside UpdateAll.
func (z *zypperBackend) UpdatePackage(pkg string, report ProgressFunc, pkgReport PackageProgressFunc) error {
	return runZypperTransactionXML([]string{"--non-interactive", "update", "-y", "--", pkg}, report, pkgReport,
		"Iniciando atualização...", "Atualização concluída")
}

func (z *zypperBackend) ClearCache(report ProgressFunc) error {
	return runStreamingCommand("zypper", []string{"clean", "--all"}, report,
		"Limpando cache...", "Cache limpo")
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

// zypperKeyBlockRe matches the "New repository or package signing key
// received" block zypper prints (and, in --non-interactive mode, rejects by
// default) when a repo's metadata is signed by a key it doesn't trust yet.
var (
	zypperKeyFingerprintRe = regexp.MustCompile(`(?m)^\s*Key Fingerprint:\s*(.+)$`)
	zypperKeyNameRe        = regexp.MustCompile(`(?m)^\s*Key Name:\s*(.+)$`)
)

// parseZypperUntrustedKey extracts the key details from zypper's rejected-key
// output, or returns ok=false if out doesn't look like that specific failure
// (a plain network/typo error should just be returned as a normal error).
func parseZypperUntrustedKey(repo, out string) (*UntrustedKeyError, bool) {
	fpMatch := zypperKeyFingerprintRe.FindStringSubmatch(out)
	if fpMatch == nil {
		return nil, false
	}
	fingerprint := strings.TrimSpace(fpMatch[1])
	name := ""
	if nameMatch := zypperKeyNameRe.FindStringSubmatch(out); nameMatch != nil {
		name = strings.TrimSpace(nameMatch[1])
	}
	keyId, err := normalizeKeyFingerprint(fingerprint)
	if err != nil {
		return nil, false
	}
	return &UntrustedKeyError{Repo: repo, KeyId: keyId, Fingerprint: fingerprint, UserId: name}, true
}

// AddRepo registers repo name via `zypper addrepo` and immediately tries to
// refresh it. A brand-new/untrusted signing key makes the refresh fail in
// --non-interactive mode (zypper auto-rejects rather than prompting) — that
// specific failure is surfaced as *UntrustedKeyError so the caller can offer
// the user a TrustRepoKey retry instead of a dead-end error.
func (z *zypperBackend) AddRepo(name, url string, report ProgressFunc) error {
	z.keyMu.Lock()
	defer z.keyMu.Unlock()
	report(0, "Adicionando repositório...")
	out, err := runCommandOutput("zypper", "--non-interactive", "addrepo", "--refresh", "--", url, name)
	if err != nil {
		return fmt.Errorf("zypper addrepo %s: %w — %s", name, err, out)
	}

	report(50, "Atualizando metadados do repositório...")
	if err := z.proposeRepoKey(name); err != nil {
		return err
	}
	report(100, "Repositório adicionado")
	return nil
}

// TrustRepoKey answers only the specific signing-key prompt reviewed by the
// caller. It never enables blanket import or disables signature verification.
func (z *zypperBackend) TrustRepoKey(repo, keyId string, report ProgressFunc) error {
	z.keyMu.Lock()
	defer z.keyMu.Unlock()
	fingerprint, err := normalizeKeyFingerprint(keyId)
	if err != nil {
		return err
	}
	identity, err := readRepoKeyIdentity(repo)
	if err != nil {
		return err
	}
	approval, ok := z.pendingKeys[repo]
	if !ok || approval.Fingerprint != fingerprint || approval.Identity != identity {
		// A daemon restart or changed repository requires a new review. The
		// existing RepoKeyPending signal carries the refreshed full fingerprint.
		return z.proposeRepoKey(repo)
	}
	delete(z.pendingKeys, repo)
	report(0, "Confiando na chave e atualizando repositório...")
	if err := refreshWithApprovedKey(repo, approval); err != nil {
		return err
	}
	report(100, "Repositório confiável e atualizado")
	return nil
}
