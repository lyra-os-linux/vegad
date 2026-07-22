package distro

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// dnfBackend drives Fedora's DNF as the PackageBackend, the same pragmatic
// CLI-shelling approach pacmanBackend/zypperBackend take for Arch/openSUSE.
// Not exercised against a live Fedora install (no Fedora machine available
// while writing this) — command shapes are based on documented dnf/rpm
// output, treat as a starting point to confirm, not a guarantee, same
// caveat openSUSEHardwareBackend already carries for its NVIDIA packages.
type dnfBackend struct{}

func newDnfBackend() *dnfBackend { return &dnfBackend{} }

func (d *dnfBackend) Name() string { return "DNF" }

// dnfInstalledSet returns the set of currently installed package names via
// rpm rather than dnf itself — Fedora is RPM-based like openSUSE, so this
// is identical in spirit to zypperInstalledSet.
func dnfInstalledSet() (map[string]bool, error) {
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

// dnfKnownArches strips the ".<arch>" suffix dnf appends to package names
// in `dnf search`/`dnf list` output (e.g. "vim-enhanced.x86_64"), so the
// rest of the backend can work with plain package names like every other
// PackageRef.Id in this codebase.
var dnfKnownArches = []string{"x86_64", "noarch", "i686", "aarch64", "armv7hl", "src"}

func stripDnfArch(name string) string {
	idx := strings.LastIndex(name, ".")
	if idx <= 0 {
		return name
	}
	suffix := name[idx+1:]
	for _, arch := range dnfKnownArches {
		if suffix == arch {
			return name[:idx]
		}
	}
	return name
}

// Search shells out to `dnf search`, which — like `zypper search`/`pacman
// -Ss` — only reads already-cached repo metadata, no network access. Output
// is "name.arch : summary" lines under one or more "=== ... Matched: ... ==="
// section headers; this skips the headers and any line without " : ".
func (d *dnfBackend) Search(query string) ([]PackageRef, error) {
	installed, err := dnfInstalledSet()
	if err != nil {
		return nil, err
	}

	out, err := runCommandOutput("dnf", "-q", "search", "--", query)
	if err != nil {
		// dnf exits non-zero with no results when nothing matches — not a
		// real error condition for a search.
		if _, ok := err.(*exec.ExitError); ok {
			return nil, nil
		}
		return nil, err
	}

	var results []PackageRef
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(strings.TrimSpace(line), "=") {
			continue
		}
		idx := strings.Index(line, " : ")
		if idx <= 0 {
			continue
		}
		name := stripDnfArch(strings.TrimSpace(line[:idx]))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		results = append(results, PackageRef{
			Origin:      "official",
			Id:          name,
			Name:        name,
			Description: strings.TrimSpace(line[idx+3:]),
			Installed:   installed[name],
			Icon:        FindPackageIcon(name),
		})
	}
	return results, scanner.Err()
}

// ListInstalled reports every RPM-installed package via `rpm -qa`, cheaper
// than asking dnf to cross-reference repo metadata for a plain "what's on
// disk" listing — identical approach to zypperBackend.ListInstalled.
func (d *dnfBackend) ListInstalled() ([]PackageRef, error) {
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

// SyncDatabase refreshes dnf's repo metadata cache — touches the network
// and needs root, same restriction as pacmanBackend/zypperBackend's
// SyncDatabase.
func (d *dnfBackend) SyncDatabase() error {
	out, err := runCommandOutput("dnf", "-y", "makecache", "--refresh")
	if err != nil {
		return fmt.Errorf("dnf makecache: %w — %s", err, out)
	}
	return nil
}

// ListUpdates reports pending updates via `dnf list --upgrades`, not `dnf
// check-update` — check-update uses special exit codes (100 = updates
// available, 0 = none, 1 = error) that would need extra handling, while
// `list --upgrades` behaves like a normal list command (exit 0 regardless
// of whether the list is empty). Only the available version is shown
// (no "current → new" arrow like zypperBackend.ListUpdates), since getting
// the installed version here would mean one extra rpm query per package.
func (d *dnfBackend) ListUpdates() ([]PackageRef, error) {
	out, err := runCommandOutput("dnf", "-q", "list", "--upgrades")
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return nil, nil
		}
		return nil, err
	}

	var results []PackageRef
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "Last metadata") || strings.HasPrefix(line, "Available Upgrades") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := stripDnfArch(fields[0])
		if name == "" {
			continue
		}
		results = append(results, PackageRef{
			Origin:      "official",
			Id:          name,
			Name:        name,
			Description: fmt.Sprintf("Atualização disponível: %s", fields[1]),
			Installed:   true,
			Icon:        FindPackageIcon(name),
		})
	}
	return results, scanner.Err()
}

// GetDetails layers `rpm -q` (for the installed view) on top of `dnf info`
// (for the repo view — available version, download size), same two-source
// approach as zypperBackend.GetDetails since both are RPM-based.
func (d *dnfBackend) GetDetails(id string) (PackageDetails, error) {
	details := PackageDetails{Origin: "official", Id: id}

	installed, err := dnfInstalledSet()
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

	if out, err := runCommandOutput("dnf", "-q", "info", "--", id); err == nil {
		// dnf info's "Key : Value" layout parses the same way as zypper
		// info's — parseZypperInfoBlock is a generic colon-block parser
		// despite the name, reused here rather than duplicated.
		info := parseZypperInfoBlock(out)
		if details.Name == "" {
			details.Name = info["Name"]
			details.Description = info["Summary"]
		}
		version := info["Version"]
		if release := info["Release"]; release != "" {
			version = version + "-" + release
		}
		if version != "" {
			details.AvailableVersion = version
		}
		if size := info["Size"]; size != "" {
			details.DownloadSize = size
		}
	}

	return details, nil
}

func (d *dnfBackend) Install(pkg string, report ProgressFunc) error {
	return runStreamingCommand("dnf", []string{"-y", "install", "--", pkg}, report,
		"Iniciando instalação...", "Instalação concluída")
}

func (d *dnfBackend) Remove(pkg string, report ProgressFunc) error {
	return runStreamingCommand("dnf", []string{"-y", "remove", "--", pkg}, report,
		"Iniciando remoção...", "Remoção concluída")
}

// UpdateAll runs `dnf upgrade`, upgrading already-installed packages — the
// DNF analogue of `zypper update`/`pacman -Syu`.
func (d *dnfBackend) UpdateAll(report ProgressFunc) error {
	return runStreamingCommand("dnf", []string{"-y", "upgrade"}, report,
		"Iniciando atualização...", "Atualização concluída")
}

func (d *dnfBackend) ClearCache(report ProgressFunc) error {
	return runStreamingCommand("dnf", []string{"clean", "all"}, report,
		"Limpando cache...", "Cache limpo")
}

// OptimizeMirrors has no DNF equivalent to expose: Fedora's mirrorlist/
// metalink metadata already picks a nearby mirror per request (via
// mirrors.fedoraproject.org), same situation as openSUSE's download
// redirector — see zypperBackend.OptimizeMirrors.
func (d *dnfBackend) OptimizeMirrors(report ProgressFunc) error {
	return ErrUnsupported
}

// ListRepos parses `dnf repolist --all`'s fixed-width "repo id / repo name
// / status" table. Only the first whitespace-separated field (the repo id,
// which never contains spaces) is taken, since the name/status columns
// aren't delimited the way zypper's "|" table is.
func (d *dnfBackend) ListRepos() ([]RepositoryRef, error) {
	out, err := runCommandOutput("dnf", "-q", "repolist", "--all")
	if err != nil {
		return nil, fmt.Errorf("dnf repolist: %w — %s", err, out)
	}

	var repos []RepositoryRef
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "repo id") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		repos = append(repos, RepositoryRef{
			Name:    fields[0],
			Enabled: strings.EqualFold(fields[len(fields)-1], "enabled"),
		})
	}
	return repos, scanner.Err()
}

// SetRepoEnabled uses the config-manager plugin (dnf-plugins-core, part of
// Fedora Workstation's default install) rather than hand-editing a .repo
// file — same reasoning zypperBackend.SetRepoEnabled gives for preferring
// the package manager's own repo-toggle subcommand over munging config.
func (d *dnfBackend) SetRepoEnabled(repo string, enabled bool) error {
	flag := "--set-disabled"
	if enabled {
		flag = "--set-enabled"
	}
	out, err := runCommandOutput("dnf", "config-manager", flag, "--", repo)
	if err != nil {
		return fmt.Errorf("dnf config-manager %s %s: %w — %s", flag, repo, err, out)
	}
	return nil
}

// dnfRepoFile is the .repo file AddRepo/TrustRepoKey write directly (rather
// than `dnf config-manager --add-repo`, which derives its own repo id from
// the URL and won't honor a user-chosen name) — same repo-id-conflicts-with-
// config-manager tradeoff noted for pacmanBackend.SetRepoEnabled's direct
// pacman.conf edits.
func dnfRepoFile(name string) string {
	return "/etc/yum.repos.d/" + name + ".repo"
}

// dnfRepoKeyURL is the conventional location RPM repo tooling (including
// OBS) publishes a repo's signing key at, next to its signed metadata —
// confirmed against a real, unrelated OBS project (openSUSE:Factory) while
// building this: <repo-url>/repodata/repomd.xml.key.
func dnfRepoKeyURL(url string) string {
	return strings.TrimRight(url, "/") + "/repodata/repomd.xml.key"
}

func writeDnfRepoFile(name, url string, gpgCheck bool, gpgKeyURL string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]\n", name)
	fmt.Fprintf(&b, "name=%s\n", name)
	fmt.Fprintf(&b, "baseurl=%s\n", url)
	b.WriteString("enabled=1\n")
	if gpgCheck {
		b.WriteString("gpgcheck=1\n")
		b.WriteString("repo_gpgcheck=1\n")
		fmt.Fprintf(&b, "gpgkey=%s\n", gpgKeyURL)
	} else {
		b.WriteString("gpgcheck=0\n")
		b.WriteString("repo_gpgcheck=0\n")
	}
	return os.WriteFile(dnfRepoFile(name), []byte(b.String()), 0o644)
}

// AddRepo writes name's .repo file directly. If the repo publishes a
// signing key at the conventional repomd.xml.key location, the key is
// fetched and previewed (not imported) — *UntrustedKeyError carries its
// fingerprint/userId for the caller to show the user before TrustRepoKey
// actually imports it. A repo with no key there is added unsigned
// (gpgcheck=0), not blocked — dnf has no way to discover a key at an
// arbitrary, unadvertised location.
func (d *dnfBackend) AddRepo(name, url string, report ProgressFunc) error {
	report(0, "Verificando chave de assinatura do repositório...")
	keyURL := dnfRepoKeyURL(url)
	keyData, found := fetchRepoKey(keyURL)
	if !found {
		report(50, "Nenhuma chave encontrada — adicionando repositório sem verificação")
		if err := writeDnfRepoFile(name, url, false, ""); err != nil {
			return fmt.Errorf("escrever %s: %w", dnfRepoFile(name), err)
		}
		report(100, "Repositório adicionado (não verificado)")
		return nil
	}

	fingerprint, userId, err := inspectGPGKey(keyData)
	if err != nil {
		return fmt.Errorf("dnf addrepo %s: %w", name, err)
	}
	return &UntrustedKeyError{Repo: name, KeyId: keyURL, Fingerprint: fingerprint, UserId: userId}
}

// TrustRepoKey finalizes the .repo file with gpgcheck enabled, pointing
// gpgkey at keyId (AddRepo passes the key URL itself as keyId, since that's
// what dnf's gpgkey= line needs — unlike zypper's opaque round-tripped
// token, this one is load-bearing) and imports the key via rpm so the next
// dnf operation against this repo doesn't re-prompt.
func (d *dnfBackend) TrustRepoKey(repo, keyId string, report ProgressFunc) error {
	report(0, "Confiando na chave...")
	out, err := runCommandOutput("rpm", "--import", keyId)
	if err != nil {
		return fmt.Errorf("rpm --import %s: %w — %s", keyId, err, out)
	}

	report(60, "Atualizando repositório...")
	baseurl, findErr := dnfRepoBaseURL(repo)
	if findErr != nil {
		return findErr
	}
	if err := writeDnfRepoFile(repo, baseurl, true, keyId); err != nil {
		return fmt.Errorf("escrever %s: %w", dnfRepoFile(repo), err)
	}

	out, err = runCommandOutput("dnf", "-q", "makecache", "--repo", repo)
	if err != nil {
		return fmt.Errorf("dnf makecache --repo %s: %w — %s", repo, err, out)
	}
	report(100, "Repositório confiável e atualizado")
	return nil
}

// dnfRepoBaseURL re-reads the baseurl= line AddRepo just wrote to repo's
// .repo file, so TrustRepoKey doesn't need that value passed back in from
// the D-Bus caller (which only round-trips repo/keyId, per RepoKeyPending).
func dnfRepoBaseURL(repo string) (string, error) {
	data, err := os.ReadFile(dnfRepoFile(repo))
	if err != nil {
		return "", fmt.Errorf("ler %s: %w", dnfRepoFile(repo), err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "baseurl="); ok {
			return v, nil
		}
	}
	return "", fmt.Errorf("%s: baseurl= não encontrado", dnfRepoFile(repo))
}
