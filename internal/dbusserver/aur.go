package dbusserver

import (
	"fmt"
	"os"
	"path/filepath"
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
