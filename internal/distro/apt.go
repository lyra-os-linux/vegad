package distro

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// aptBackend drives Debian/Ubuntu's apt/dpkg as the PackageBackend, the
// same pragmatic CLI-shelling approach pacmanBackend/zypperBackend take for
// Arch/openSUSE. Unlike those two, this has not been exercised against a
// live Debian/Ubuntu install — treat the exact command output parsing as a
// documented starting point to confirm, not a guarantee, same caveat
// hardware_opensuse.go already carries for its NVIDIA package names.
type aptBackend struct{}

func newAptBackend() *aptBackend { return &aptBackend{} }

func (a *aptBackend) Name() string { return "APT" }

// aptEnv forces non-interactive mode so apt-get never blocks waiting on a
// debconf prompt (e.g. a package's EULA or config-file conflict dialog) —
// there is no terminal attached to answer it, same reasoning as
// commandEnvC forcing LC_ALL=C for locale-independent parsing.
func aptEnv() []string {
	return append(append(os.Environ(), "DEBIAN_FRONTEND=noninteractive"), "LC_ALL=C")
}

// aptGetCommand builds an apt-get *exec.Cmd with the non-interactive
// environment already set, shared by callers that need CombinedOutput
// rather than the streaming progress path (e.g. KernelBackend.Remove,
// which has no ProgressFunc in its interface signature).
func aptGetCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("apt-get", args...)
	cmd.Env = aptEnv()
	return cmd
}

func runAptGet(args []string, report ProgressFunc, startMsg, doneMsg string) error {
	return runStreamingCmd(aptGetCommand(args...), report, startMsg, doneMsg)
}

// aptInstalledSet returns the set of currently installed package names via
// dpkg-query, cheaper than asking apt to cross-reference repo metadata for
// a plain membership check — same role zypperInstalledSet plays for Zypper.
func aptInstalledSet() (map[string]bool, error) {
	out, err := runCommandOutput("dpkg-query", "-W", "-f", "${Package}\n")
	if err != nil {
		// dpkg-query exits non-zero when the dpkg database has never been
		// touched (fresh chroot) — not a real error for an empty set.
		if _, ok := err.(*exec.ExitError); ok && out == "" {
			return map[string]bool{}, nil
		}
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

// aptInstalledDetails reports installed packages with their one-line
// summary, via dpkg's own "${binary:Summary}" virtual field — this is
// dpkg-native (derived from the package's Description first line),
// available without apt/apt-utils installed.
func aptInstalledDetails() ([]PackageRef, error) {
	out, err := runCommandOutput("dpkg-query", "-W", "-f", "${Package}\t${binary:Summary}\n")
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok && out == "" {
			return nil, nil
		}
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

func (a *aptBackend) ListInstalled() ([]PackageRef, error) {
	return aptInstalledDetails()
}

// aptCacheSearchLine matches one "name - description" row from `apt-cache
// search`, which lists every match regardless of install state.
var aptCacheSearchLine = regexp.MustCompile(`^(\S+)\s+-\s+(.*)$`)

func (a *aptBackend) Search(query string) ([]PackageRef, error) {
	installed, err := aptInstalledSet()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("apt-cache", "search", "--", query)
	cmd.Env = aptEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("apt-cache search: %w — %s", err, strings.TrimSpace(string(out)))
	}

	var results []PackageRef
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		match := aptCacheSearchLine.FindStringSubmatch(scanner.Text())
		if match == nil {
			continue
		}
		name := match[1]
		results = append(results, PackageRef{
			Origin:      "official",
			Id:          name,
			Name:        name,
			Description: match[2],
			Installed:   installed[name],
			Icon:        FindPackageIcon(name),
		})
	}
	return results, scanner.Err()
}

// SyncDatabase runs `apt-get update`, refreshing repo metadata — touches
// the network and needs root, same restriction as the Arch/openSUSE
// backends' SyncDatabase.
func (a *aptBackend) SyncDatabase() error {
	cmd := exec.Command("apt-get", "update")
	cmd.Env = aptEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("apt-get update: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// aptUpgradableLine matches one row of `apt list --upgradable`, formatted
// as "name/suite version arch [upgradable from: oldversion]".
var aptUpgradableLine = regexp.MustCompile(`^(\S+)/\S+\s+(\S+)\s+\S+\s+\[upgradable from:\s*(\S+)\]`)

func (a *aptBackend) ListUpdates() ([]PackageRef, error) {
	cmd := exec.Command("apt", "list", "--upgradable")
	cmd.Env = aptEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("apt list --upgradable: %w — %s", err, strings.TrimSpace(string(out)))
	}

	var results []PackageRef
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Listing...") {
			continue
		}
		match := aptUpgradableLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		name, newVersion, oldVersion := match[1], match[2], match[3]
		results = append(results, PackageRef{
			Origin:      "official",
			Id:          name,
			Name:        name,
			Description: fmt.Sprintf("%s → %s", oldVersion, newVersion),
			Installed:   true,
			Icon:        FindPackageIcon(name),
		})
	}
	return results, scanner.Err()
}

// parseAptCacheShowBlock parses the "Key: Value" layout of `apt-cache
// show`'s first stanza (a package can have multiple stanzas across repos —
// only the first, highest-priority one is used, matching `apt-get
// install`'s own candidate selection).
func parseAptCacheShowBlock(out string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			break // end of first stanza
		}
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		if _, exists := fields[key]; exists {
			continue // keep only the first stanza's values
		}
		fields[key] = strings.TrimSpace(line[idx+1:])
	}
	return fields
}

func (a *aptBackend) GetDetails(id string) (PackageDetails, error) {
	details := PackageDetails{Origin: "official", Id: id}

	installed, err := aptInstalledSet()
	if err != nil {
		return details, err
	}
	details.Installed = installed[id]

	if details.Installed {
		out, err := runCommandOutput("dpkg-query", "-W", "-f",
			"${Package}\t${Version}\t${binary:Summary}\t${Installed-Size}\n", "--", id)
		if err == nil {
			fields := strings.SplitN(out, "\t", 4)
			if len(fields) == 4 {
				details.Name = fields[0]
				details.InstalledVersion = fields[1]
				details.AvailableVersion = fields[1]
				details.Description = fields[2]
				// Installed-Size is reported in KiB by dpkg.
				details.InstalledSize = humanizeAptKibibytes(fields[3])
			}
		}
	}

	cmd := exec.Command("apt-cache", "show", "--", id)
	cmd.Env = aptEnv()
	if out, err := cmd.CombinedOutput(); err == nil {
		info := parseAptCacheShowBlock(string(out))
		if details.Name == "" {
			details.Name = info["Package"]
			details.Description = info["Description"]
		}
		if v := info["Version"]; v != "" {
			details.AvailableVersion = v
		}
		if size := info["Size"]; size != "" {
			details.DownloadSize = humanizeBytes(size)
		}
		if maint := info["Maintainer"]; maint != "" {
			details.Maintainer = maint
		}
		if url := info["Homepage"]; url != "" {
			details.URL = url
		}
		if lic := info["License"]; lic != "" {
			details.Licenses = []string{lic}
		}
	}

	return details, nil
}

func humanizeAptKibibytes(raw string) string {
	return humanizeBytes(strings.TrimSpace(raw) + "000") // KiB reported without unit; approximate to bytes for the shared formatter
}

func (a *aptBackend) Install(pkg string, report ProgressFunc) error {
	return runAptGet([]string{"install", "-y", "--", pkg}, report,
		"Iniciando instalação...", "Instalação concluída")
}

func (a *aptBackend) Remove(pkg string, report ProgressFunc) error {
	return runAptGet([]string{"remove", "-y", "--", pkg}, report,
		"Iniciando remoção...", "Remoção concluída")
}

// UpdateAll runs `apt-get upgrade`, upgrading already-installed packages
// without adding/removing any (the apt analogue of `zypper update`/`pacman
// -Syu`, not a full `dist-upgrade`).
func (a *aptBackend) UpdateAll(report ProgressFunc) error {
	return runAptGet([]string{"upgrade", "-y"}, report,
		"Iniciando atualização...", "Atualização concluída")
}

func (a *aptBackend) ClearCache(report ProgressFunc) error {
	return runAptGet([]string{"clean"}, report, "Limpando cache...", "Cache limpo")
}

// OptimizeMirrors has no built-in apt equivalent to expose (netselect-apt/
// apt-select are third-party, not installed by default) — same
// ErrUnsupported path Zypper's OptimizeMirrors already takes.
func (a *aptBackend) OptimizeMirrors(report ProgressFunc) error {
	return ErrUnsupported
}

// aptSourceListPaths returns every classic-format sources file apt reads —
// /etc/apt/sources.list plus any *.list under sources.list.d/. This does
// not cover the newer DEB822 *.sources format (deb822-style, e.g. Ubuntu
// 24.04+'s /etc/apt/sources.list.d/ubuntu.sources) — a documented gap, not
// silently mishandled: ListRepos/SetRepoEnabled simply won't see those
// entries yet.
func aptSourceListPaths() ([]string, error) {
	paths := []string{"/etc/apt/sources.list"}
	entries, err := os.ReadDir("/etc/apt/sources.list.d")
	if err != nil {
		if os.IsNotExist(err) {
			return paths, nil
		}
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".list") {
			paths = append(paths, "/etc/apt/sources.list.d/"+entry.Name())
		}
	}
	return paths, nil
}

// aptRepoLine reports whether line is an active or disabled "deb"/"deb-src"
// entry, and its content stripped of any disabling "# " prefix.
func aptRepoLine(line string) (content string, enabled bool, isRepo bool) {
	trimmed := strings.TrimSpace(line)
	uncommented := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
	if strings.HasPrefix(uncommented, "deb ") || strings.HasPrefix(uncommented, "deb-src ") {
		return uncommented, !strings.HasPrefix(trimmed, "#"), true
	}
	return "", false, false
}

func (a *aptBackend) ListRepos() ([]RepositoryRef, error) {
	paths, err := aptSourceListPaths()
	if err != nil {
		return nil, err
	}
	var repos []RepositoryRef
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if content, enabled, ok := aptRepoLine(line); ok {
				repos = append(repos, RepositoryRef{Name: content, Enabled: enabled})
			}
		}
	}
	return repos, nil
}

// SetRepoEnabled comments/uncomments the matching "deb"/"deb-src" line
// directly in the sources files — apt has no single command equivalent to
// `zypper modifyrepo`. This only recognizes an entry whose line matches repo
// exactly (as returned by ListRepos); it does not resolve PPA shorthand
// (`ppa:user/name`) the way `add-apt-repository` does — a deliberately
// reduced scope for this first pass, not a silent limitation.
func (a *aptBackend) SetRepoEnabled(repo string, enabled bool) error {
	paths, err := aptSourceListPaths()
	if err != nil {
		return err
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		changed := false
		for i, line := range lines {
			content, lineEnabled, isRepo := aptRepoLine(line)
			if !isRepo || content != repo || lineEnabled == enabled {
				continue
			}
			if enabled {
				lines[i] = content
			} else {
				lines[i] = "# " + content
			}
			changed = true
		}
		if changed {
			return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
		}
	}
	return fmt.Errorf("apt: repositório %q não encontrado", repo)
}

// aptRepoFile is the sources.list.d file AddRepo/TrustRepoKey write directly
// — same one-file-per-repo layout add-apt-repository itself uses for PPAs.
func aptRepoFile(name string) string {
	return "/etc/apt/sources.list.d/" + name + ".list"
}

// aptKeyringFile is where TrustRepoKey stores the imported (dearmored)
// public key, referenced from the sources.list.d entry via the modern
// signed-by= option — deliberately not using the deprecated system-wide
// apt-key/trusted.gpg.d, which trusts a key for every repo instead of just
// this one.
func aptKeyringFile(name string) string {
	return "/etc/apt/keyrings/" + name + ".gpg"
}

// aptReleaseKeyURL is the conventional location Debian/Ubuntu repos
// (including OBS's Debian/Ubuntu targets) publish their signing key at, for
// the "flat repo" layout (`deb <url> /`, no dists/ hierarchy) AddRepo uses.
func aptReleaseKeyURL(url string) string {
	return strings.TrimRight(url, "/") + "/Release.key"
}

// writeAptRepoLine (re)writes name's sources.list.d entry as a flat repo
// (`deb [options] <url> /`), the layout OBS publishes Debian/Ubuntu targets
// in — no dists/<suite>/ hierarchy, so there's no separate suite/component
// to ask the user for beyond the URL they already gave AddRepo.
func writeAptRepoLine(name, url string, signed bool) error {
	line := "deb "
	if signed {
		line += "[signed-by=" + aptKeyringFile(name) + "] "
	}
	line += url + " /\n"
	return os.WriteFile(aptRepoFile(name), []byte(line), 0644)
}

// aptRepoLineURL re-reads the URL out of name's own sources.list.d entry —
// same "read back what AddRepo already wrote" trick dnfRepoBaseURL uses, so
// TrustRepoKey doesn't need the URL passed back in from the D-Bus caller.
func aptRepoLineURL(name string) (string, error) {
	data, err := os.ReadFile(aptRepoFile(name))
	if err != nil {
		return "", fmt.Errorf("ler %s: %w", aptRepoFile(name), err)
	}
	content, _, isRepo := aptRepoLine(strings.TrimSpace(string(data)))
	if !isRepo {
		return "", fmt.Errorf("%s: entrada deb inválida", aptRepoFile(name))
	}
	fields := strings.Fields(content)
	// fields[0]="deb", optionally fields[1]="[signed-by=...]", then the URL.
	for _, f := range fields[1:] {
		if !strings.HasPrefix(f, "[") {
			return f, nil
		}
	}
	return "", fmt.Errorf("%s: URL não encontrada na entrada deb", aptRepoFile(name))
}

// AddRepo writes name's sources.list.d entry as a flat repo (`deb <url> /`)
// right away — apt has no single "add repo" command, unlike zypper/dnf.
// If the repo publishes a signing key at the conventional Release.key
// location, it's fetched and previewed (not imported yet): *UntrustedKeyError
// carries its fingerprint/userId for the caller to show the user before
// TrustRepoKey actually imports it. No key found there means the repo is
// left unsigned (apt has no way to discover a key at an arbitrary,
// unadvertised location) and `apt-get update` runs immediately.
func (a *aptBackend) AddRepo(name, url string, report ProgressFunc) error {
	report(0, "Adicionando repositório...")
	if err := writeAptRepoLine(name, url, false); err != nil {
		return fmt.Errorf("escrever %s: %w", aptRepoFile(name), err)
	}

	report(30, "Verificando chave de assinatura do repositório...")
	keyURL := aptReleaseKeyURL(url)
	keyData, found := fetchRepoKey(keyURL)
	if !found {
		report(60, "Nenhuma chave encontrada — atualizando repositório sem verificação")
		if err := runAptGet([]string{"update"}, report, "Atualizando índices...", "Repositório adicionado (não verificado)"); err != nil {
			return err
		}
		return nil
	}

	fingerprint, userId, err := inspectGPGKey(keyData)
	if err != nil {
		return fmt.Errorf("apt addrepo %s: %w", name, err)
	}
	return &UntrustedKeyError{Repo: name, KeyId: keyURL, Fingerprint: fingerprint, UserId: userId}
}

// TrustRepoKey re-fetches keyId (AddRepo passes the key URL itself, same
// load-bearing use as dnfBackend.TrustRepoKey), dearmors it into
// aptKeyringFile, rewrites the sources.list.d entry to reference it via
// signed-by=, and refreshes.
func (a *aptBackend) TrustRepoKey(repo, keyId string, report ProgressFunc) error {
	report(0, "Confiando na chave...")
	keyData, found := fetchRepoKey(keyId)
	if !found {
		return fmt.Errorf("apt: não foi possível baixar novamente a chave em %s", keyId)
	}

	if err := os.MkdirAll("/etc/apt/keyrings", 0755); err != nil {
		return fmt.Errorf("criar /etc/apt/keyrings: %w", err)
	}
	dearmor := exec.Command("gpg", "--dearmor", "--yes", "--output", aptKeyringFile(repo))
	dearmor.Stdin = strings.NewReader(string(keyData))
	if out, err := dearmor.CombinedOutput(); err != nil {
		return fmt.Errorf("gpg --dearmor: %w — %s", err, strings.TrimSpace(string(out)))
	}

	report(50, "Atualizando repositório...")
	url, err := aptRepoLineURL(repo)
	if err != nil {
		return err
	}
	if err := writeAptRepoLine(repo, url, true); err != nil {
		return fmt.Errorf("escrever %s: %w", aptRepoFile(repo), err)
	}

	return runAptGet([]string{"update"}, report, "Atualizando índices...", "Repositório confiável e atualizado")
}
