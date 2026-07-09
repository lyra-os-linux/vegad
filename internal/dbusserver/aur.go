package dbusserver

import (
	"fmt"
	"os"
	"path/filepath"
)

// aurBuildHome is vega-build's home directory (packaging/vegad/sysusers.d/vega-build.conf),
// used as both the sandbox's writable path and the AUR helper's cache/config HOME.
const aurBuildHome = "/var/lib/vega/build"

// aurHelper picks whichever AUR helper is installed, preferring paru over
// yay when both are present. Neither ships with Lyra OS by default — this is
// an optdepend (packaging/vegad/PKGBUILD) the user installs to unlock the
// "Comunidade" origin.
func aurHelper() (string, error) {
	for _, name := range []string{"paru", "yay"} {
		if commandAvailable(name) {
			return name, nil
		}
	}
	return "", fmt.Errorf("nenhum ajudante AUR encontrado — instale yay ou paru")
}

// vegaBuildSystemdRunArgs builds the systemd-run(1) properties that sandbox
// an AUR helper invocation as the unprivileged vega-build user (never root —
// PROMPT-VEGA.md §2.3). Search and PKGBUILD fetch never write outside
// aurBuildHome and never need elevation, so they get the strictest
// profile: NoNewPrivileges plus ProtectSystem=strict (whole root read-only
// except aurBuildHome).
//
// A real install can't use that profile: it needs the helper's own
// internal `sudo pacman` step, which (a) requires NoNewPrivileges off —
// that setting blocks setuid binaries like sudo from gaining privileges
// at all — and (b) writes wherever the package's file list says to
// (/usr, /etc, /var/lib/pacman, ...), which is the entire point of
// `pacman -U` and can't be pre-enumerated into a ReadWritePaths allowlist.
// So ProtectSystem is dropped too for this one case; vega-build's own
// Unix permissions (not this mount-namespace layer) are what keeps the
// PKGBUILD's build()/package() steps from touching the rest of the
// system — that boundary is unaffected either way.
func vegaBuildSystemdRunArgs(allowSudo bool) []string {
	args := []string{
		"--wait", "--collect", "--pipe", "--quiet",
		"-p", "User=vega-build",
		"-p", "WorkingDirectory=" + aurBuildHome,
		"-p", "Environment=HOME=" + aurBuildHome,
		"-p", "PrivateTmp=yes",
		"-p", "PrivateDevices=yes",
		"-p", "ProtectHome=read-only",
	}
	if allowSudo {
		args = append(args, "-p", "ReadWritePaths=/run")
	} else {
		args = append(args,
			"-p", "NoNewPrivileges=yes",
			"-p", "ProtectSystem=strict",
			"-p", "ReadWritePaths="+aurBuildHome,
		)
	}
	return args
}

// searchAur shells out to `<helper> -Ssa`, which queries the AUR RPC
// directly (network) restricted to AUR-only hits — official repo hits are
// already covered by searchPacman, so mixing them in here would just
// duplicate results. Best-effort: an AUR helper error (no matches, RPC
// hiccup) degrades to an empty result rather than failing the whole search,
// same as when no helper is installed at all.
func searchAur(query string) ([]PackageRef, error) {
	helper, err := aurHelper()
	if err != nil {
		return nil, nil
	}

	installed, err := pacmanInstalledSet()
	if err != nil {
		return nil, err
	}

	args := append(vegaBuildSystemdRunArgs(false), "--", helper, "-Ssa", "--color=never", "--", query)
	out, _ := runCommandOutput("systemd-run", args...)
	return parseSearchOutput([]byte(out), "aur", installed), nil
}

// fetchAurPkgbuild clones/updates the AUR git checkout for pkgbase via
// `<helper> -G` (fetch only, no build) and returns the PKGBUILD contents so
// the UI can show it for review before the user confirms an install
// (PROMPT-VEGA.md §2.3).
func fetchAurPkgbuild(pkgbase string) (string, error) {
	helper, err := aurHelper()
	if err != nil {
		return "", err
	}

	args := append(vegaBuildSystemdRunArgs(false), "--", helper, "-G", "--", pkgbase)
	if out, err := runCommandOutput("systemd-run", args...); err != nil {
		return "", fmt.Errorf("%s -G %s: %w — %s", helper, pkgbase, err, out)
	}

	data, err := os.ReadFile(filepath.Join(aurBuildHome, pkgbase, "PKGBUILD"))
	if err != nil {
		return "", fmt.Errorf("PKGBUILD não encontrado após fetch de %s: %w", pkgbase, err)
	}
	return string(data), nil
}

// installAurPackage runs `<helper> -S`, letting it handle fetch, sandboxed
// build (as vega-build, never root) and the final `pacman -U` in one go —
// the last step needs the sudoers NOPASSWD rule granted to vega-build
// (packaging/vegad/sudoers.d/vega-build).
func installAurPackage(pkgbase string, report progressFunc) error {
	helper, err := aurHelper()
	if err != nil {
		return err
	}

	args := append(vegaBuildSystemdRunArgs(true), "--", helper, "-S", "--noconfirm", "--needed", "--cleanafter", "--", pkgbase)
	return runStreamingCommand("systemd-run", args, report, "Iniciando instalação AUR ("+helper+")...", "Instalação AUR concluída")
}
