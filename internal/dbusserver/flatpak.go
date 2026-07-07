package dbusserver

import (
	"bufio"
	"fmt"
	"os/exec"
	"strings"
)

// searchFlatpak shells out to `flatpak search`, which queries the locally
// cached appstream data for configured remotes (Flathub) without requiring
// elevated privileges.
func searchFlatpak(query string) ([]PackageRef, error) {
	installed, err := flatpakInstalledApps()
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
		})
	}
	return results, scanner.Err()
}

// flatpakInstalledApps maps installed app IDs to their friendly names, used
// both to filter update listings and to label results without an extra
// remote round-trip per app.
func flatpakInstalledApps() (map[string]string, error) {
	cmd := exec.Command("flatpak", "list", "--app", "--system", "--columns=application,name")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	apps := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) < 2 {
			continue
		}
		apps[fields[0]] = fields[1]
	}
	return apps, scanner.Err()
}

// listFlatpakUpdates has no clean "list only" subcommand in this flatpak
// version (1.18) — `flatpak update` always mixes the pending list with an
// interactive confirmation. We run it answering "n" (so nothing is ever
// applied) and check which installed app IDs appear in the pre-confirmation
// output, sidestepping locale-specific column parsing.
func listFlatpakUpdates() ([]PackageRef, error) {
	installed, err := flatpakInstalledApps()
	if err != nil {
		return nil, err
	}
	if len(installed) == 0 {
		return nil, nil
	}

	cmd := exec.Command("flatpak", "update", "--system")
	cmd.Stdin = strings.NewReader("n\n")
	out, _ := cmd.CombinedOutput() // exit status is meaningless here: "n" always makes it exit non-zero

	var results []PackageRef
	text := string(out)
	for id, name := range installed {
		if strings.Contains(text, id) {
			results = append(results, PackageRef{
				Origin:      "flathub",
				Id:          id,
				Name:        name,
				Description: "Atualização disponível",
				Installed:   true,
			})
		}
	}
	return results, nil
}

// installFlatpak installs an app from Flathub into the system-wide
// installation.
func installFlatpak(appID string, report progressFunc) error {
	return runStreamingCommand(
		"flatpak", []string{"install", "-y", "--noninteractive", "--system", "flathub", appID},
		report, "Iniciando instalação...", "Concluído",
	)
}

// removeFlatpak uninstalls a system-wide Flatpak app.
func removeFlatpak(appID string, report progressFunc) error {
	return runStreamingCommand(
		"flatpak", []string{"uninstall", "-y", "--noninteractive", "--system", appID},
		report, "Iniciando remoção...", "Concluído",
	)
}

// updateAllFlatpak updates every installed Flatpak app to its latest
// available version.
func updateAllFlatpak(report progressFunc) error {
	return runStreamingCommand(
		"flatpak", []string{"update", "-y", "--noninteractive", "--system"},
		report, "Verificando atualizações...", "Concluído",
	)
}

// clearFlatpakCache removes runtimes/extensions no longer required by any
// installed app (PROMPT-VEGA.md §3.1 "runtimes Flatpak órfãos").
func clearFlatpakCache(report progressFunc) error {
	return runStreamingCommand(
		"flatpak", []string{"uninstall", "--unused", "-y", "--noninteractive", "--system"},
		report, "Procurando runtimes órfãos...", "Concluído",
	)
}

// runStreamingCommand runs a subprocess and reports coarse, monotonically
// increasing progress as it emits output lines — flatpak's real progress
// bars use carriage returns rather than newlines, so this can't track exact
// percentages, only "it's moving" milestones.
func runStreamingCommand(name string, args []string, report progressFunc, startMsg, doneMsg string) error {
	report(0, startMsg)

	cmd := exec.Command(name, args...)
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
