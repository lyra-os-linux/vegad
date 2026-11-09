package distro

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ID identifies the package backend of the running system.
type ID int

const (
	Unknown ID = iota
	// OpenSUSELeap is the backend of Lyra OS, which is built on openSUSE
	// Leap: zypper, Snapper and the Leap kernel packages.
	OpenSUSELeap
)

func (d ID) String() string {
	switch d {
	case OpenSUSELeap:
		return "opensuse-leap"
	default:
		return "unknown"
	}
}

// osReleasePath is a var, not const, so tests can point at a fixture file
// instead of the real /etc/os-release.
var osReleasePath = "/etc/os-release"

// Detect reads /etc/os-release's ID. vegad is exclusive to Lyra OS (the
// Desktop, ID=lyra-os, and the Server, ID=lyra-os-server); every other
// system — openSUSE Leap, Tumbleweed, SLES or another derivative — is
// refused (decided by the maintainer on 09/11/2026, vegad#132). ID_LIKE is
// deliberately ignored: it would admit any SUSE-like system. Callers fail
// startup clearly instead of running on a system vegad was not made for.
func Detect() (ID, error) {
	fields, err := parseOSRelease(osReleasePath)
	if err != nil {
		return Unknown, fmt.Errorf("distro: não foi possível ler %s: %w", osReleasePath, err)
	}
	if id, ok := idFromName(fields["ID"]); ok {
		return id, nil
	}
	return Unknown, fmt.Errorf("distro: o vegad é exclusivo do Lyra OS (ID=%q ID_LIKE=%q)", fields["ID"], fields["ID_LIKE"])
}

// PrettyName reports the running distro's human-readable name and version
// (e.g. "openSUSE Leap 16.0"), straight from /etc/os-release's PRETTY_NAME —
// this is for display only (About screen, logs), never for branching logic,
// which must go through Detect/ID instead.
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
	case "lyra-os", "lyra-os-server":
		return OpenSUSELeap, true
	default:
		return Unknown, false
	}
}

// parseOSRelease parses the KEY=value (optionally quoted) lines of an
// os-release file, per the freedesktop.org spec.
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
