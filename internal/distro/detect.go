package distro

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ID identifies a supported Linux distribution family.
type ID int

const (
	Unknown ID = iota
	Arch
	OpenSUSELeap
	// Debian covers both Debian and Ubuntu (and derivatives resolved via
	// ID_LIKE, e.g. Pop!_OS, Linux Mint) — the package-manager mechanics
	// (apt/dpkg) are identical for our purposes, so one Provider serves
	// the whole family.
	Debian
	Fedora
)

func (d ID) String() string {
	switch d {
	case Arch:
		return "arch"
	case OpenSUSELeap:
		return "opensuse-leap"
	case Debian:
		return "debian"
	case Fedora:
		return "fedora"
	default:
		return "unknown"
	}
}

// osReleasePath is a var, not const, so tests can point at a fixture file
// instead of the real /etc/os-release.
var osReleasePath = "/etc/os-release"

// Detect reads /etc/os-release's ID (falling back to ID_LIKE for
// derivatives) to identify the running distro family. It returns an error
// for anything other than the distros vegad currently supports, rather than
// guessing — callers must fail startup clearly instead of silently running
// the wrong package manager commands.
func Detect() (ID, error) {
	fields, err := parseOSRelease(osReleasePath)
	if err != nil {
		return Unknown, fmt.Errorf("distro: não foi possível ler %s: %w", osReleasePath, err)
	}

	if id, ok := idFromName(fields["ID"]); ok {
		return id, nil
	}

	for _, like := range strings.Fields(fields["ID_LIKE"]) {
		if id, ok := idFromName(like); ok {
			return id, nil
		}
	}

	return Unknown, fmt.Errorf("distro: distribuição não suportada (ID=%q ID_LIKE=%q)", fields["ID"], fields["ID_LIKE"])
}

// PrettyName reports the running distro's human-readable name and version
// (e.g. "openSUSE Leap 16.0", "Arch Linux"), straight from /etc/os-release's
// PRETTY_NAME — this is for display only (About screen, logs), never for
// branching logic, which must go through Detect/ID instead.
func PrettyName() string {
	fields, err := parseOSRelease(osReleasePath)
	if err != nil || fields["PRETTY_NAME"] == "" {
		if id, detectErr := Detect(); detectErr == nil {
			return id.String()
		}
		return "desconhecida"
	}
	return fields["PRETTY_NAME"]
}

func idFromName(name string) (ID, bool) {
	switch name {
	case "arch":
		return Arch, true
	case "opensuse-leap", "suse", "opensuse":
		return OpenSUSELeap, true
	case "debian", "ubuntu":
		return Debian, true
	case "fedora":
		return Fedora, true
	default:
		return Unknown, false
	}
}

// parseOSRelease parses the KEY=value (optionally quoted) lines of an
// os-release file, per the freedesktop.org spec that both Arch and openSUSE
// follow.
func parseOSRelease(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fields := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}
		key := line[:idx]
		value := strings.Trim(strings.TrimSpace(line[idx+1:]), `"`)
		fields[key] = value
	}
	return fields, scanner.Err()
}
