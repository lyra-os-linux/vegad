package diagnostics

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The security test (lyra-ecosystem openspec change add-security-review,
// capability security-diagnostics): read-only checks of exposure and
// configuration, with safe remedies for what can be fixed.

// serverExpected are exposures the server profile is built around: SSH and
// the vega-web panel.
func serverExpected(socket ListeningSocket) bool {
	return socket.Allow == "service:ssh" || socket.Port == 22 || socket.Port == 9090
}

type zypperUpdates struct {
	Updates []struct {
		Kind     string `xml:"kind,attr"`
		Name     string `xml:"name,attr"`
		Category string `xml:"category,attr"`
		Severity string `xml:"severity,attr"`
	} `xml:"update-status>update-list>update"`
}

// securityPatches parses `zypper --xmlout list-patches --category security`.
func securityPatches(out []byte) ([]string, error) {
	start := strings.Index(string(out), "<stream>")
	if start < 0 {
		return nil, fmt.Errorf("saída do zypper sem <stream>")
	}
	var parsed zypperUpdates
	if err := xml.Unmarshal(out[start:], &parsed); err != nil {
		return nil, err
	}
	var names []string
	for _, update := range parsed.Updates {
		if update.Kind == "patch" {
			label := update.Name
			if update.Severity != "" {
				label += " (" + update.Severity + ")"
			}
			names = append(names, label)
		}
	}
	return names, nil
}

// sshdSettings reads `sshd -T` (lowercase keys).
func sshdSettings(out string) map[string]string {
	settings := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			settings[key] = strings.TrimSpace(value)
		}
	}
	return settings
}

type lsblkTree struct {
	Blockdevices []lsblkNode `json:"blockdevices"`
}

type lsblkNode struct {
	Name     string      `json:"name"`
	Type     string      `json:"type"`
	Children []lsblkNode `json:"children"`
}

// hasCrypt walks the inverse tree of `lsblk -J -s` (the root device and its
// parents) looking for a LUKS layer.
func hasCrypt(nodes []lsblkNode) bool {
	for _, node := range nodes {
		if node.Type == "crypt" || hasCrypt(node.Children) {
			return true
		}
	}
	return false
}

type account struct {
	name string
	uid  int
}

// humanAccounts lists accounts with UID 1000..59999 from /etc/passwd.
func humanAccounts(passwd string) []account {
	var accounts []account
	for _, line := range strings.Split(passwd, "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 7 {
			continue
		}
		uid, err := strconv.Atoi(fields[2])
		if err == nil && uid >= 1000 && uid < 60000 {
			accounts = append(accounts, account{fields[0], uid})
		}
	}
	return accounts
}

// emptyPasswords returns the accounts whose /etc/shadow field is empty.
// Only names leave this function, never a hash.
func emptyPasswords(shadow string, accounts []account) []string {
	human := map[string]bool{}
	for _, a := range accounts {
		human[a.name] = true
	}
	var empty []string
	for _, line := range strings.Split(shadow, "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 2 && human[fields[0]] && fields[1] == "" {
			empty = append(empty, fields[0])
		}
	}
	return empty
}

// groupMembers returns the members of group from /etc/group.
func groupMembers(groups, group string) map[string]bool {
	members := map[string]bool{}
	for _, line := range strings.Split(groups, "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 4 && fields[0] == group {
			for _, name := range strings.Split(fields[3], ",") {
				if name != "" {
					members[name] = true
				}
			}
		}
	}
	return members
}

func runSecurity(ctx context.Context, env *Env, progress Progress) StageResult {
	start := time.Now()
	result := StageResult{ID: "security", Metrics: map[string]any{}}
	var findings []Finding
	server := env.Profile == "server"
	failedTo := func(tool string, err error) StageResult {
		return failedToRun(env, result, tool, err, start)
	}

	progress(5, env.t("diagnostics.progress.security.firewall"))
	running := firewalldActive(ctx, env)
	result.Metrics["firewall"] = running
	if ctx.Err() != nil {
		return failedTo("systemctl", ctx.Err())
	}
	if !running {
		off := env.finding(SeverityAttention, "security.firewall-off", "")
		off.Remedy = "enable-firewall"
		findings = append(findings, off)
	}

	progress(20, env.t("diagnostics.progress.security.ports"))
	sockets, err := ListListening(ctx, env)
	if err != nil {
		return failedTo("ss", err)
	}
	exposed := 0
	for _, socket := range sockets {
		if !socket.isExposedNetwork() {
			continue
		}
		exposed++
		// The service name when there is one, else the program, without its
		// directory: what the user recognizes.
		who := filepath.Base(socket.Executable)
		if socket.Unit != "" {
			who = socket.Unit
		} else if socket.Executable == "" {
			who = "?"
		}
		raw := fmt.Sprintf("%s %s:%d %s zona=%s %s", socket.Protocol, socket.Address, socket.Port, who, socket.Zone, socket.Allow)
		args := []string{"port", strconv.Itoa(int(socket.Port)) + "/" + socket.Protocol, "program", who}
		if server && serverExpected(socket) {
			findings = append(findings, env.finding(Info, "security.exposed-expected", raw, args...))
			continue
		}
		f := env.finding(SeverityAttention, "security.exposed-port", raw, args...)
		// Closing needs a zone and what in it opened the port; a zone that
		// accepts everything (target) is not closed port by port.
		if running && socket.Zone != "" && (strings.HasPrefix(socket.Allow, "service:") || strings.HasPrefix(socket.Allow, "port:")) {
			f.Remedy, f.RemedyArgs = "close-port", []string{socket.Zone + "|" + socket.Allow}
		}
		findings = append(findings, f)
	}
	result.Metrics["listening"] = len(sockets)
	result.Metrics["exposed"] = exposed

	progress(40, env.t("diagnostics.progress.security.patches"))
	if _, err := env.LookPath("zypper"); err == nil {
		out, _, err := env.Run(ctx, "zypper", "-n", "--no-refresh", "--xmlout", "list-patches", "--category", "security")
		if err != nil {
			return failedTo("zypper", err)
		}
		if patches, err := securityPatches(out); err == nil {
			result.Metrics["securityPatches"] = len(patches)
			if len(patches) > 0 {
				f := env.finding(SeverityAttention, "security.patches-pending", strings.Join(patches, "\n"), "count", strconv.Itoa(len(patches)))
				f.Remedy = "apply-security-patches"
				findings = append(findings, f)
			}
		}
	}

	progress(55, env.t("diagnostics.progress.security.ssh"))
	enabled, _, _ := env.Run(ctx, "systemctl", "is-enabled", "sshd")
	active, _, _ := env.Run(ctx, "systemctl", "is-active", "sshd")
	sshOn := strings.TrimSpace(string(enabled)) == "enabled" || strings.TrimSpace(string(active)) == "active"
	result.Metrics["sshd"] = sshOn
	if sshOn {
		out, code, err := env.Run(ctx, "sshd", "-T")
		switch {
		case err != nil && ctx.Err() != nil:
			return failedTo("sshd", err)
		case err != nil || code != 0:
			findings = append(findings, env.finding(Info, "security.ssh-unreadable", lastLines(string(out), 6)))
		default:
			settings := sshdSettings(string(out))
			if settings["permitrootlogin"] == "yes" {
				findings = append(findings, env.finding(SeverityAttention, "security.ssh-root-password", "PermitRootLogin yes"))
			}
			if settings["passwordauthentication"] == "yes" {
				severity := SeverityAttention
				if server {
					severity = Info
				}
				findings = append(findings, env.finding(severity, "security.ssh-password", "PasswordAuthentication yes"))
			}
		}
	}

	progress(70, env.t("diagnostics.progress.security.system"))
	mode := "disabled"
	if out, _, err := env.Run(ctx, "getenforce"); err == nil {
		mode = strings.ToLower(strings.TrimSpace(string(out)))
	} else if enforce := readTrimmed(env, "/sys/fs/selinux/enforce"); enforce != "" {
		mode = map[string]string{"1": "enforcing", "0": "permissive"}[enforce]
	}
	result.Metrics["selinux"] = mode
	if mode != "enforcing" {
		// Informative until the Desktop edition records its SELinux policy
		// in an ADR (open question 1 of the proposal).
		findings = append(findings, env.finding(Info, "security.selinux", mode, "mode", env.t("diagnostics.selinux."+mode)))
	}
	if _, err := env.LookPath("mokutil"); err == nil {
		out, _, _ := env.Run(ctx, "mokutil", "--sb-state")
		on := strings.Contains(string(out), "SecureBoot enabled")
		result.Metrics["secureBoot"] = on
		if !on {
			findings = append(findings, env.finding(Info, "security.secure-boot-off", strings.TrimSpace(string(out))))
		}
	}
	if source, _, err := env.Run(ctx, "findmnt", "-n", "-o", "SOURCE", "/"); err == nil {
		device := strings.TrimSpace(string(source))
		if i := strings.IndexByte(device, '['); i >= 0 {
			device = device[:i]
		}
		if strings.HasPrefix(device, "/dev/") {
			if tree, _, err := env.Run(ctx, "lsblk", "-J", "-s", "-o", "NAME,TYPE", device); err == nil {
				var parsed lsblkTree
				if json.Unmarshal(tree, &parsed) == nil {
					encrypted := hasCrypt(parsed.Blockdevices)
					result.Metrics["rootEncrypted"] = encrypted
					if !encrypted {
						findings = append(findings, env.finding(Info, "security.disk-unencrypted", device))
					}
				}
			}
		}
	}

	progress(85, env.t("diagnostics.progress.security.accounts"))
	accounts := humanAccounts(readTrimmed(env, "/etc/passwd"))
	if shadow, err := env.ReadFile("/etc/shadow"); err == nil {
		if empty := emptyPasswords(string(shadow), accounts); len(empty) > 0 {
			findings = append(findings, env.finding(SeverityFail, "security.empty-password", strings.Join(empty, "\n"), "accounts", strings.Join(empty, ", ")))
			result.Sensitive = append(result.Sensitive, empty...)
		}
	}
	wheel := groupMembers(readTrimmed(env, "/etc/group"), "wheel")
	var open []string
	for _, a := range accounts {
		if wheel[a.name] {
			continue
		}
		out, _, err := env.Run(ctx, "sudo", "-n", "-l", "-U", a.name)
		if err == nil && strings.Contains(string(out), "may run the following commands") {
			open = append(open, a.name)
		}
	}
	if len(open) > 0 {
		findings = append(findings, env.finding(SeverityFail, "security.sudo-open", strings.Join(open, "\n"), "accounts", strings.Join(open, ", ")))
		result.Sensitive = append(result.Sensitive, open...)
	}

	result.Findings = findings
	result.Verdict = verdictFromFindings(findings)
	result.Summary = env.t("diagnostics.summary.security", "exposed", strconv.Itoa(exposed), "count", strconv.Itoa(len(findings)))
	result.DurationSeconds = int(time.Since(start).Seconds())
	return result
}

func (s ListeningSocket) isExposedNetwork() bool {
	return s.Reach == "network" && s.State == "exposed"
}
