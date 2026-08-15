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
)

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

// Load reads the profile from VEGAD_PROFILE first, then from path. Desktop is
// the compatibility default for installations made before profiles existed.
func Load(path string) (Profile, string, error) {
	if value, ok := os.LookupEnv("VEGAD_PROFILE"); ok {
		p, err := Parse(value)
		return p, "VEGAD_PROFILE", err
	}

	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return Desktop, "compatibility default", nil
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
	return Desktop, "compatibility default", nil
}

func (p Profile) Capabilities() []string {
	common := []string{"backup", "datetime", "firewall", "hardware", "kernel", "logs", "monitor", "network", "packages-native", "services", "snapshots", "storage", "users"}
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
