package dbusserver

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const aurSourceRootEnv = "VEGA_AUR_SOURCE_ROOT"

func installAurPackage(pkgbase string, report progressFunc) error {
	root := os.Getenv(aurSourceRootEnv)
	if root == "" {
		return fmt.Errorf("AUR indisponível: defina %s com a raiz dos checkouts", aurSourceRootEnv)
	}

	sourceDir := filepath.Join(root, pkgbase)
	if _, err := os.Stat(filepath.Join(sourceDir, "PKGBUILD")); err != nil {
		return fmt.Errorf("PKGBUILD não encontrado em %s: %w", sourceDir, err)
	}

	report(0, "Iniciando build AUR isolado...")
	if err := runAurBuild(sourceDir, report); err != nil {
		return err
	}

	pkgfile, err := latestBuiltPackage(sourceDir, pkgbase)
	if err != nil {
		return err
	}

	report(85, "Instalando pacote resultante...")
	return runPacmanTransaction([]string{"-U", "--noconfirm", "--", pkgfile}, report)
}

func searchAur(query string) ([]PackageRef, error) {
	root := os.Getenv(aurSourceRootEnv)
	if root == "" {
		return nil, nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, nil
	}

	var results []PackageRef
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sourceDir := filepath.Join(root, entry.Name())
		pkgname, desc, ok := aurMetadata(sourceDir)
		if !ok {
			continue
		}
		needle := strings.ToLower(strings.Join([]string{entry.Name(), pkgname, desc}, " "))
		if !strings.Contains(needle, query) {
			continue
		}
		results = append(results, PackageRef{
			Origin:      "aur",
			Id:          entry.Name(),
			Name:        firstNonEmpty(pkgname, entry.Name()),
			Description: firstNonEmpty(desc, "Pacote AUR local"),
			Installed:   false,
		})
	}
	return results, nil
}

func aurMetadata(sourceDir string) (string, string, bool) {
	pkgbuild := filepath.Join(sourceDir, "PKGBUILD")
	data, err := os.ReadFile(pkgbuild)
	if err != nil {
		return "", "", false
	}

	var pkgname, desc string
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	reName := regexp.MustCompile(`^\s*pkgname\s*=\s*([^#\n]+)`)
	reDesc := regexp.MustCompile(`^\s*pkgdesc\s*=\s*['"]?([^'"\n]+)`)
	for scanner.Scan() {
		line := scanner.Text()
		if pkgname == "" {
			if m := reName.FindStringSubmatch(line); m != nil {
				pkgname = strings.Trim(strings.TrimSpace(m[1]), "(')\"")
			}
		}
		if desc == "" {
			if m := reDesc.FindStringSubmatch(line); m != nil {
				desc = strings.Trim(strings.TrimSpace(m[1]), "(')\"")
			}
		}
		if pkgname != "" && desc != "" {
			break
		}
	}
	if pkgname == "" {
		pkgname = filepath.Base(sourceDir)
	}
	return pkgname, desc, true
}

func runAurBuild(sourceDir string, report progressFunc) error {
	args := []string{
		"--wait",
		"--collect",
		"--pipe",
		"--quiet",
		"-p", "User=vega-build",
		"-p", "WorkingDirectory=" + sourceDir,
		"-p", "Environment=HOME=/var/lib/vega/build",
		"-p", "PrivateTmp=yes",
		"-p", "PrivateDevices=yes",
		"-p", "NoNewPrivileges=yes",
		"-p", "ProtectSystem=strict",
		"-p", "ProtectHome=read-only",
		"-p", "ReadWritePaths=" + sourceDir,
		"-p", "ReadWritePaths=/var/lib/vega/build",
		"-p", "ReadWritePaths=/tmp",
		"--",
		"makepkg",
		"-f",
		"--noconfirm",
		"--nodeps",
	}
	return runStreamingCommand("systemd-run", args, report, "Iniciando build AUR...", "Build AUR concluído")
}

func latestBuiltPackage(sourceDir, pkgbase string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(sourceDir, pkgbase+"-[0-9]*.pkg.tar.zst"))
	if err != nil {
		return "", err
	}
	var selected string
	var selectedInfo os.FileInfo
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			continue
		}
		if selected == "" || info.ModTime().After(selectedInfo.ModTime()) || (info.ModTime().Equal(selectedInfo.ModTime()) && strings.Compare(match, selected) > 0) {
			selected = match
			selectedInfo = info
		}
	}
	if selected == "" {
		return "", fmt.Errorf("nenhum pacote AUR gerado em %s", sourceDir)
	}
	return selected, nil
}
