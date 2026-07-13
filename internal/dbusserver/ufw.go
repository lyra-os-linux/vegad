package dbusserver

import (
	"bufio"
	"regexp"
	"sort"
	"strings"
)

// ufw.go is Firewall's Debian/Ubuntu counterpart to firewalld.go — ufw
// ships no D-Bus API either, same CLI-only situation snapper.go already
// deals with for Snapshots. FirewallService picks whichever of the two is
// present at call time (see firewall.go), not by distro ID, matching the
// "tool presence" pattern already established for Snapshots.

func ufwInstalled() bool {
	return commandAvailable("ufw")
}

// ufwCatalog maps a handful of well-known ufw application profiles (each
// shipped by its owning package, e.g. "OpenSSH" by openssh-server) to the
// same friendly Portuguese labels firewall.go's catalog already uses for
// firewalld service ids — kept deliberately small since, unlike firewalld's
// bundled service definitions, ufw profiles only exist for packages that
// are actually installed.
var ufwCatalog = []struct {
	name  string
	label string
}{
	{name: "OpenSSH", label: "Acesso remoto (SSH)"},
	{name: "Samba", label: "Compartilhamento de arquivos"},
	{name: "CUPS", label: "Impressoras"},
}

// ufwRuleLine matches a `ufw status` rule row, where the "To" column (app
// name or port) is left-padded with two or more spaces before the action
// keyword — ufw's table is fixed-width, not delimiter-separated, so this
// can't just split on whitespace (app names like "Apache Full" contain a
// space themselves).
var ufwRuleLine = regexp.MustCompile(`^(.+?)\s{2,}(ALLOW|DENY|REJECT|LIMIT)\b`)

func ufwEnabledApps() map[string]bool {
	enabled := map[string]bool{}
	out, err := runCommandOutput("ufw", "status")
	if err != nil {
		return enabled
	}
	for _, line := range strings.Split(out, "\n") {
		match := ufwRuleLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		name := strings.TrimSpace(match[1])
		name = strings.TrimSuffix(name, " (v6)")
		enabled[name] = true
	}
	return enabled
}

func ufwAvailableApps() map[string]bool {
	available := map[string]bool{}
	out, err := runCommandOutput("ufw", "app", "list")
	if err != nil {
		return available
	}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasSuffix(line, ":") { // "Available applications:"
			continue
		}
		available[line] = true
	}
	return available
}

func parseUfwStatusActive(out string) bool {
	return strings.HasPrefix(strings.TrimSpace(out), "Status: active")
}

func ufwStatus() (bool, string) {
	out, err := runCommandOutput("ufw", "status")
	if err != nil {
		return false, ""
	}
	// ufw has no zone concept like firewalld, so the second return value
	// (kept only for signature parity with firewalldStatus) is always "".
	return parseUfwStatusActive(out), ""
}

func ufwListServices() []FirewallServiceInfo {
	enabled := ufwEnabledApps()
	available := ufwAvailableApps()

	var rows []FirewallServiceInfo
	seen := map[string]bool{}
	for _, item := range ufwCatalog {
		if !available[item.name] {
			continue
		}
		rows = append(rows, FirewallServiceInfo{Name: item.name, Label: item.label, Enabled: enabled[item.name]})
		seen[item.name] = true
	}
	for name := range available {
		if seen[name] {
			continue
		}
		rows = append(rows, FirewallServiceInfo{Name: name, Label: name, Enabled: enabled[name]})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

// ufwSetServiceEnabled adds/removes an ALLOW rule for the named app
// profile. Unlike `ufw enable`/`ufw --force reset`, plain rule add/remove
// doesn't prompt for confirmation, so no --force is needed here.
func ufwSetServiceEnabled(name string, enabled bool) error {
	if enabled {
		return runCommand("ufw", "allow", name)
	}
	return runCommand("ufw", "delete", "allow", name)
}
