// Package profile defines the trusted, host-wide vegad execution profile.
package profile

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

type Profile string

const (
	Desktop Profile = "desktop"
	Server  Profile = "server"

	DefaultConfigPath = "/etc/vega/vegad.conf"

	// ServerMarkerPath identifies a Lyra OS Server installation. The Server
	// image renders it from release-server.toml (lyraos-server); desktop
	// editions ship /usr/lib/lyra-os/release instead.
	ServerMarkerPath = "/usr/lib/lyra-os/server-release"
)

// serverMarkerPath is replaced by tests.
var serverMarkerPath = ServerMarkerPath

func Parse(value string) (Profile, error) {
	switch Profile(strings.ToLower(strings.TrimSpace(value))) {
	case Desktop:
		return Desktop, nil
	case Server:
		return Server, nil
	default:
		return "", fmt.Errorf("perfil vegad inválido %q (use desktop ou server)", value)
	}
}

// Load reads the profile from VEGAD_PROFILE first, then from path. When
// neither configures it, the installed edition decides: the Server marker
// selects server and everything else keeps the desktop compatibility default
// for installations made before profiles existed.
func Load(path string) (Profile, string, error) {
	if value, ok := os.LookupEnv("VEGAD_PROFILE"); ok {
		p, err := Parse(value)
		return p, "VEGAD_PROFILE", err
	}

	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return detectEdition()
	}
	if err != nil {
		return "", path, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if strings.TrimSpace(key) != "VEGAD_PROFILE" || !found {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		p, err := Parse(value)
		return p, path, err
	}
	if err := scanner.Err(); err != nil {
		return "", path, err
	}
	return detectEdition()
}

// detectEdition selects the profile from the installed edition when no
// configuration named one. The Server image carries ServerMarkerPath, so a
// fresh Lyra OS Server runs the server profile without any extra file in the
// image (lyraos-server#31); every other installation stays on desktop.
func detectEdition() (Profile, string, error) {
	if info, err := os.Stat(serverMarkerPath); err == nil && info.Mode().IsRegular() {
		return Server, "edition marker " + serverMarkerPath, nil
	}
	return Desktop, "compatibility default", nil
}

func (p Profile) Capabilities() []string {
	common := []string{"backup", "datetime", "firewall", "hardware", "kernel", "logs", "monitor", "network", "packages-native", "services", "snapshots", "storage", "users", "nvidia-official-v1", "nvidia-recovery-v1", "diagnostics-v1", "diagnostics-v2", "diagnostics-v3", "network-exposure-v1", "space-cleanup-v1", "diagnostics-actions-v1", "diagnostics-v4", "diagnostics-v5"}
	if p == Desktop {
		return append(common, "bluetooth", "flatpak", "session-desktop")
	}
	return common
}

func (p Profile) Has(capability string) bool {
	for _, current := range p.Capabilities() {
		if current == capability {
			return true
		}
	}
	return false
}
