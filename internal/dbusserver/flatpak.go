package dbusserver

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/lyraos/vegad/internal/distro"
)

var flatpakAppIDPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)+\.[A-Za-z_][A-Za-z0-9_-]*$`)

func validateFlatpakAppID(id string) error {
	if len(id) > 255 || !flatpakAppIDPattern.MatchString(id) {
		return fmt.Errorf("identificador de aplicativo Flatpak inválido: %q", id)
	}
	return nil
}

func flatpakAppCommand(operation, appID, scope string, u *desktopUser) (*exec.Cmd, error) {
	if err := validateFlatpakAppID(appID); err != nil {
		return nil, err
	}
	args := []string{operation, "-y", "--noninteractive", "--app"}
	switch scope {
	case "user":
		if u == nil || u.Uid == 0 || u.Username == "" {
			return nil, fmt.Errorf("operação Flatpak user exige identidade do chamador")
		}
		return flatpakUserCmd(u, append(args, "--user", "--", appID)...), nil
	case "system":
		args = append(args, "--system", "--")
		if operation == "install" {
			args = append(args, "flathub")
		}
		return exec.Command("flatpak", append(args, appID)...), nil
	default:
		return nil, fmt.Errorf("escopo Flatpak inválido: %q", scope)
	}
}

// flatpakApp is one entry from `flatpak list`, tagged with which
// installation it came from so removal/update targets the right one.
type flatpakApp struct {
	Name  string
	Scope string // "system" or "user"
}

// flatpakUserCmd builds a `flatpak ... --user` invocation that runs as the
// resolved desktop user rather than root, so it reads/writes that user's own
// ~/.local/share/flatpak instead of root's. The UID/GID transition is made by
// runuser after exec, instead of exec.Cmd.SysProcAttr.Credential during fork.
// The latter is rejected with EPERM in the hardened vegad systemd service on
// Leap 16, before flatpak itself has a chance to start.
func flatpakUserCmd(u *desktopUser, args ...string) *exec.Cmd {
	runuserArgs := []string{"--user", u.Username, "--", "/usr/bin/flatpak"}
	runuserArgs = append(runuserArgs, args...)
	cmd := exec.Command("/usr/sbin/runuser", runuserArgs...)
	env := environmentWith(os.Environ(), "HOME", u.HomeDir)
	if _, err := os.Stat(u.RuntimeDir); err == nil {
		env = environmentWith(env, "XDG_RUNTIME_DIR", u.RuntimeDir)
	}
	cmd.Env = env
	return cmd
}

// environmentWith replaces an existing value as well as adding a missing
// one. Appending HOME to os.Environ leaves duplicate entries, and libc-based
// programs may keep the first one (root's HOME) instead of the intended user.
func environmentWith(env []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

// searchFlatpak shells out to `flatpak search`, which queries the locally
// cached appstream data for configured remotes (Flathub) without requiring
// elevated privileges. u is the resolved desktop user (nil if it couldn't be
// resolved), used only to mark results already installed in that user's own
// --user scope.
func searchFlatpak(query string, u *desktopUser) ([]PackageRef, error) {
	installed, err := flatpakInstalledApps(u)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("flatpak", "search", "--columns=name,description,application", "--", query)
	out, err := cmd.Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok && len(out) == 0 {
			return nil, nil
		}
		return nil, err
	}

	var results []PackageRef
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) < 3 {
			continue
		}
		_, isInstalled := installed[fields[2]]
		results = append(results, PackageRef{
			Origin:      "flathub",
			Id:          fields[2],
			Name:        fields[0],
			Description: fields[1],
			Installed:   isInstalled,
			Icon:        findFlatpakIcon(fields[2], u),
		})
	}
	return results, scanner.Err()
}

// flatpakInstalledApps maps installed app IDs to their friendly name and
// scope, used to filter update/search listings and to label results without
// an extra remote round-trip per app. It always checks the system-wide
// installation; the per-user installation is only checked when u is
// resolved — vegad runs as root, so without a resolved desktop user
// `flatpak --user` would only ever see root's own installation, not the
// caller's.
func flatpakInstalledApps(u *desktopUser) (map[string]flatpakApp, error) {
	apps := map[string]flatpakApp{}
	systemCmd := exec.Command("flatpak", "list", "--app", "--system", "--columns=application,name")
	if err := collectFlatpakInstalled(systemCmd, "system", apps); err != nil {
		return nil, err
	}
	if u != nil {
		userCmd := flatpakUserCmd(u, "list", "--app", "--user", "--columns=application,name")
		if err := collectFlatpakInstalled(userCmd, "user", apps); err != nil {
			log.Printf("vegad: flatpak list --user (%s): %v", u.Username, err)
		}
	}
	return apps, nil
}

func collectFlatpakInstalled(cmd *exec.Cmd, scope string, apps map[string]flatpakApp) error {
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) < 2 {
			continue
		}
		apps[fields[0]] = flatpakApp{Name: fields[1], Scope: scope}
	}
	return scanner.Err()
}

func listFlatpakInstalled(u *desktopUser) ([]PackageRef, error) {
	apps, err := flatpakInstalledApps(u)
	if err != nil {
		return nil, err
	}

	var results []PackageRef
	for id, app := range apps {
		results = append(results, PackageRef{
			Origin:    "flathub",
			Id:        id,
			Name:      app.Name,
			Installed: true,
			Icon:      findFlatpakIcon(id, u),
		})
	}
	return results, nil
}

// listFlatpakUpdates asks each resolved scope what it would update. flatpak
// 1.16 has no dry-run: `flatpak update` always mixes the pending list with an
// interactive confirmation, and neither --no-pull nor --no-deploy is a
// read-only probe (one deploys from cache, the other downloads). So we run it
// answering "n", which never applies anything, and read the plan it prints
// first. `flatpak remote-ls --updates` would be read-only by construction but
// measured ~4x slower here, so it is not worth the swap while this sits
// behind the update caches.
//
// A failing scope is returned as an error rather than silently reported as
// "nothing pending" — callers decide whether to degrade, and per
// SoftwareService.ListUpdates a broken Flatpak must not hide native updates.
func listFlatpakUpdates(u *desktopUser) ([]PackageRef, error) {
	apps, err := flatpakInstalledApps(u)
	if err != nil {
		return nil, err
	}
	if len(apps) == 0 {
		return nil, nil
	}

	pending := map[string]bool{}
	var failures []error
	if err := collectPendingFlatpakUpdates(
		exec.Command("flatpak", "update", "--system"), apps, "system", pending); err != nil {
		failures = append(failures, err)
	}
	if u != nil {
		if err := collectPendingFlatpakUpdates(
			flatpakUserCmd(u, "update", "--user"), apps, "user", pending); err != nil {
			failures = append(failures, err)
		}
	}

	var results []PackageRef
	for id := range pending {
		app := apps[id]
		results = append(results, PackageRef{
			Origin:     "flathub",
			Id:         id,
			Name:       app.Name,
			Installed:  true,
			Icon:       findFlatpakIcon(id, u),
			Repository: "Flathub",
		})
	}
	return results, errors.Join(failures...)
}

// collectPendingFlatpakUpdates records which of scope's installed apps the
// update plan lists.
//
// Exit status alone cannot separate the two non-zero cases: declining the
// prompt and failing outright both exit 1 (an empty plan exits 0). A run that
// named at least one installed app was a decline and is fine; a non-zero run
// that named none really failed — unreachable remote, broken installation —
// and has to surface, because reading that as "nothing pending" tells the
// user they are up to date when nothing was actually checked.
func collectPendingFlatpakUpdates(cmd *exec.Cmd, apps map[string]flatpakApp, scope string, pending map[string]bool) error {
	cmd.Stdin = strings.NewReader("n\n")
	out, err := cmd.CombinedOutput()

	found := 0
	for id, app := range apps {
		if app.Scope == scope && planListsApp(out, id) {
			pending[id] = true
			found++
		}
	}
	if err != nil && found == 0 {
		return fmt.Errorf("consultar atualizações Flatpak (%s): %w — %s",
			scope, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// planListsApp reports whether the plan names exactly this app ID. Matching
// the raw substring instead would let org.gnome.Builder.Devel's row mark
// org.gnome.Builder as pending too, so compare whole whitespace-separated
// fields. Some flatpak outputs print a full app/ID/arch/branch ref in place
// of the bare ID, so the ID slot of such a ref counts as well — only that
// slot, never the arch or branch beside it.
func planListsApp(out []byte, id string) bool {
	for _, line := range strings.Split(string(out), "\n") {
		for _, field := range strings.Fields(line) {
			if field == id {
				return true
			}
			parts := strings.Split(field, "/")
			if len(parts) == 4 && parts[0] == "app" && parts[1] == id {
				return true
			}
		}
	}
	return false
}

// findFlatpakIcon checks the system-wide export tree, then (when u is
// resolved) the desktop user's own --user export tree, before falling back
// to the distro-wide icon theme lookup.
func findFlatpakIcon(appID string, u *desktopUser) string {
	bases := []string{"/var/lib/flatpak"}
	if u != nil {
		bases = append(bases, u.HomeDir+"/.local/share/flatpak")
	}
	sizes := []string{"scalable", "256x256", "128x128", "64x64", "48x48"}
	for _, base := range bases {
		for _, size := range sizes {
			ext := ".png"
			if size == "scalable" {
				ext = ".svg"
			}
			candidate := base + "/exports/share/icons/hicolor/" + size + "/apps/" + appID + ext
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	return distro.FindPackageIcon(appID)
}

// parseFlatpakInfoBlock parses the right-aligned "Key: Value" layout of
// `flatpak info`/`flatpak remote-info` under LC_ALL=C — unlike pacman's
// left-aligned "Key : Value", the key itself is padded with leading spaces
// and the separator has no space before the colon, so this needs its own
// parser rather than reusing parsePacmanInfoBlock.
func parseFlatpakInfoBlock(out []byte) map[string]string {
	fields := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		idx := strings.Index(line, ": ")
		if idx <= 0 {
			continue
		}
		fields[line[:idx]] = strings.TrimSpace(line[idx+2:])
	}
	return fields
}

// fetchFlatpakDetails uses `flatpak info` for installed apps (has Installed
// Size but not Download Size, since nothing needs downloading) and
// `flatpak remote-info` against Flathub for everything else. An app
// installed in the desktop user's --user scope is queried as that user.
func fetchFlatpakDetails(appID string, u *desktopUser) (PackageDetails, error) {
	details := PackageDetails{Origin: "flathub", Id: appID}

	installed, err := flatpakInstalledApps(u)
	if err != nil {
		return details, err
	}

	var cmd *exec.Cmd
	if app, ok := installed[appID]; ok {
		details.Installed = true
		details.Name = app.Name
		if app.Scope == "user" {
			cmd = flatpakUserCmd(u, "info", "--user", "--", appID)
		} else {
			cmd = exec.Command("flatpak", "info", "--system", "--", appID)
		}
	} else {
		cmd = exec.Command("flatpak", "remote-info", "--system", "flathub", "--", appID)
	}
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, "LC_ALL=C")

	out, err := cmd.Output()
	if err != nil {
		return details, fmt.Errorf("flatpak info %s: %w", appID, err)
	}
	fields := parseFlatpakInfoBlock(out)
	if details.Name == "" {
		details.Name = appID
	}
	details.Licenses = distro.SplitPackageList(fields["License"])
	details.DownloadSize = fields["Download Size"]
	if details.Installed {
		details.InstalledVersion = fields["Version"]
		details.InstalledSize = fields["Installed Size"]
	} else {
		details.AvailableVersion = fields["Version"]
	}

	return details, nil
}

// installFlatpak installs an app from Flathub into the system-wide
// installation — there's no scope picker in the UI yet, so installs always
// target --system, same as before.
func installFlatpak(appID string, report progressFunc) error {
	cmd, err := flatpakAppCommand("install", appID, "system", nil)
	if err != nil {
		return err
	}
	return runStreamingCmd(
		cmd,
		report, "Iniciando instalação...", "Concluído",
	)
}

// removeFlatpak uninstalls a Flatpak app from whichever installation it was
// actually found in (see SoftwareService.Remove) — system-wide, or the
// desktop user's own --user installation when scope is "user".
func removeFlatpak(appID, scope string, u *desktopUser, report progressFunc) error {
	cmd, err := flatpakAppCommand("uninstall", appID, scope, u)
	if err != nil {
		return err
	}
	return runStreamingCmd(
		cmd,
		report, "Iniciando remoção...", "Concluído",
	)
}

// updateFlatpak updates a single installed Flatpak app, targeting whichever
// installation it was actually found in (scope, resolved the same way
// removeFlatpak does) rather than every installed app like updateAllFlatpak.
func updateFlatpak(appID, scope string, u *desktopUser, report progressFunc) error {
	cmd, err := flatpakAppCommand("update", appID, scope, u)
	if err != nil {
		return err
	}
	return runStreamingCmd(
		cmd,
		report, "Iniciando atualização...", "Concluído",
	)
}

// updateAllFlatpak updates every installed Flatpak app to its latest
// available version, in the system-wide installation and, when a desktop
// user is resolved, that user's own --user installation too.
func updateAllFlatpak(u *desktopUser, report progressFunc) error {
	if err := runStreamingCmd(
		exec.Command("flatpak", "update", "-y", "--noninteractive", "--system"),
		report, "Verificando atualizações do sistema...", "Concluído",
	); err != nil {
		return err
	}
	if u == nil {
		return nil
	}
	return runStreamingCmd(
		flatpakUserCmd(u, "update", "-y", "--noninteractive", "--user"),
		report, "Verificando atualizações do usuário...", "Concluído",
	)
}

// clearFlatpakCache removes runtimes/extensions no longer required by any
// installed app, in both the system-wide and (when resolved) the desktop
// user's --user installation.
func clearFlatpakCache(u *desktopUser, report progressFunc) error {
	if err := runStreamingCmd(
		exec.Command("flatpak", "uninstall", "--unused", "-y", "--noninteractive", "--system"),
		report, "Procurando runtimes órfãos do sistema...", "Concluído",
	); err != nil {
		return err
	}
	if u == nil {
		return nil
	}
	return runStreamingCmd(
		flatpakUserCmd(u, "uninstall", "--unused", "-y", "--noninteractive", "--user"),
		report, "Procurando runtimes órfãos do usuário...", "Concluído",
	)
}

// runStreamingCmd runs a subprocess and reports coarse, monotonically
// increasing progress as it emits output lines — flatpak's real progress
// bars use carriage returns rather than newlines, so this can't track exact
// percentages, only "it's moving" milestones.
func runStreamingCmd(cmd *exec.Cmd, report progressFunc, startMsg, doneMsg string) error {
	report(0, startMsg)

	name := cmd.Path
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Split(bufio.ScanLines)
	var lastLines []string
	percent := uint32(10)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lastLines = append(lastLines, line)
		if len(lastLines) > 20 {
			lastLines = lastLines[1:]
		}
		if percent < 90 {
			percent += 5
		}
		report(percent, line)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%s: %w — %s", name, err, strings.Join(lastLines, " | "))
	}
	report(100, doneMsg)
	return nil
}
