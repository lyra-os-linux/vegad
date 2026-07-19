package dbusserver

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/lyraos/vegad/internal/distro"
)

// flatpakApp is one entry from `flatpak list`, tagged with which
// installation it came from so removal/update targets the right one.
type flatpakApp struct {
	Name  string
	Scope string // "system" or "user"
}

// flatpakUserCmd builds a `flatpak ... --user` invocation that runs as the
// resolved desktop user rather than root, so it reads/writes that user's own
// ~/.local/share/flatpak instead of root's.
func flatpakUserCmd(u *desktopUser, args ...string) *exec.Cmd {
	cmd := exec.Command("flatpak", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: u.Uid, Gid: u.Gid},
	}
	env := append(os.Environ(), "HOME="+u.HomeDir)
	if _, err := os.Stat(u.RuntimeDir); err == nil {
		env = append(env, "XDG_RUNTIME_DIR="+u.RuntimeDir)
	}
	cmd.Env = env
	return cmd
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

// listFlatpakUpdates has no clean "list only" subcommand in this flatpak
// version (1.18) — `flatpak update` always mixes the pending list with an
// interactive confirmation. We run it once per resolved scope answering "n"
// (so nothing is ever applied) and check which of that scope's installed
// app IDs appear in the pre-confirmation output, sidestepping
// locale-specific column parsing.
func listFlatpakUpdates(u *desktopUser) ([]PackageRef, error) {
	apps, err := flatpakInstalledApps(u)
	if err != nil {
		return nil, err
	}
	if len(apps) == 0 {
		return nil, nil
	}

	pending := map[string]bool{}
	collectPendingFlatpakUpdates(exec.Command("flatpak", "update", "--system"), apps, "system", pending)
	if u != nil {
		collectPendingFlatpakUpdates(flatpakUserCmd(u, "update", "--user"), apps, "user", pending)
	}

	var results []PackageRef
	for id := range pending {
		app := apps[id]
		results = append(results, PackageRef{
			Origin:      "flathub",
			Id:          id,
			Name:        app.Name,
			Description: "Atualização disponível",
			Installed:   true,
			Icon:        findFlatpakIcon(id, u),
		})
	}
	return results, nil
}

func collectPendingFlatpakUpdates(cmd *exec.Cmd, apps map[string]flatpakApp, scope string, pending map[string]bool) {
	cmd.Stdin = strings.NewReader("n\n")
	out, _ := cmd.CombinedOutput() // exit status is meaningless here: "n" always makes it exit non-zero
	text := string(out)
	for id, app := range apps {
		if app.Scope == scope && strings.Contains(text, id) {
			pending[id] = true
		}
	}
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
	return runStreamingCmd(
		exec.Command("flatpak", "install", "-y", "--noninteractive", "--system", "flathub", appID),
		report, "Iniciando instalação...", "Concluído",
	)
}

// removeFlatpak uninstalls a Flatpak app from whichever installation it was
// actually found in (see SoftwareService.Remove) — system-wide, or the
// desktop user's own --user installation when scope is "user".
func removeFlatpak(appID, scope string, u *desktopUser, report progressFunc) error {
	if scope == "user" && u != nil {
		return runStreamingCmd(
			flatpakUserCmd(u, "uninstall", "-y", "--noninteractive", "--user", appID),
			report, "Iniciando remoção...", "Concluído",
		)
	}
	return runStreamingCmd(
		exec.Command("flatpak", "uninstall", "-y", "--noninteractive", "--system", appID),
		report, "Iniciando remoção...", "Concluído",
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
