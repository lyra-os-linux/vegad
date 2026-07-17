package dbusserver

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// graphicalSession describes the active graphical login (seat0) that display
// commands (xrandr/wlr-randr) must run inside. vegad itself always runs as
// root on the system bus (see packaging/vegad/vegad.service) but display
// configuration is scoped to a user's X11/Wayland session, not a system
// privilege — these commands run as that user via runuser, with the
// session's own DISPLAY/WAYLAND_DISPLAY/XAUTHORITY/XDG_RUNTIME_DIR forwarded
// from its leader process environment (root can read any process's
// /proc/<pid>/environ).
type graphicalSession struct {
	user string
	kind string // "x11" or "wayland"
	env  map[string]string
}

func activeGraphicalSession() (*graphicalSession, error) {
	if !commandAvailable("loginctl") {
		return nil, fmt.Errorf("loginctl não está disponível")
	}
	out, err := runCommandOutput("loginctl", "list-sessions", "--no-legend")
	if err != nil {
		return nil, fmt.Errorf("loginctl list-sessions: %s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		props, err := sessionProperties(fields[0])
		if err != nil || props["Active"] != "yes" || props["Seat"] == "" {
			continue
		}
		kind := props["Type"]
		if kind != "x11" && kind != "wayland" {
			continue
		}
		username := props["Name"]
		if username == "" {
			continue
		}
		env := leaderEnviron(props["Leader"])
		if env["XDG_RUNTIME_DIR"] == "" {
			env["XDG_RUNTIME_DIR"] = "/run/user/" + props["User"]
		}
		return &graphicalSession{user: username, kind: kind, env: env}, nil
	}
	return nil, fmt.Errorf("nenhuma sessão gráfica ativa encontrada (seat0)")
}

func sessionProperties(sessionID string) (map[string]string, error) {
	out, err := runCommandOutput("loginctl", "show-session", sessionID,
		"-p", "Active", "-p", "Seat", "-p", "Type", "-p", "Name", "-p", "User", "-p", "Leader")
	if err != nil {
		return nil, err
	}
	props := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		props[key] = value
	}
	return props, nil
}

func leaderEnviron(pid string) map[string]string {
	env := map[string]string{}
	if pid == "" || pid == "0" {
		return env
	}
	data, err := os.ReadFile("/proc/" + pid + "/environ")
	if err != nil {
		return env
	}
	for _, entry := range strings.Split(string(data), "\x00") {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch key {
		case "DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "XDG_RUNTIME_DIR":
			env[key] = value
		}
	}
	return env
}

// run executes name inside the graphical session, as the session's own
// user, with its display environment forwarded.
func (s *graphicalSession) run(name string, args ...string) (string, error) {
	if !commandAvailable("runuser") {
		return "", fmt.Errorf("runuser não está disponível")
	}
	full := append([]string{"-u", s.user, "--", name}, args...)
	cmd := exec.Command("runuser", full...)
	env := os.Environ()
	for key, value := range s.env {
		env = append(env, key+"="+value)
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (s *graphicalSession) listOutputs() ([]DisplayOutput, error) {
	switch s.kind {
	case "wayland":
		if !commandAvailable("wlr-randr") {
			return nil, fmt.Errorf("wlr-randr não está disponível (necessário em compositores Wayland baseados em wlroots; GNOME/KDE ainda não são suportados)")
		}
		out, err := s.run("wlr-randr")
		if err != nil {
			return nil, fmt.Errorf("wlr-randr: %s", out)
		}
		return parseWlrRandr(out), nil
	case "x11":
		if !commandAvailable("xrandr") {
			return nil, fmt.Errorf("xrandr não está disponível")
		}
		out, err := s.run("xrandr", "--query")
		if err != nil {
			return nil, fmt.Errorf("xrandr: %s", out)
		}
		return parseXrandr(out), nil
	default:
		return nil, fmt.Errorf("tipo de sessão desconhecido: %s", s.kind)
	}
}

func (s *graphicalSession) applyMode(output string, width, height uint32, refreshHz, scale float64, rotation string) error {
	switch s.kind {
	case "wayland":
		if !commandAvailable("wlr-randr") {
			return fmt.Errorf("wlr-randr não está disponível (necessário em compositores Wayland baseados em wlroots; GNOME/KDE ainda não são suportados)")
		}
		return applyWlrRandr(s, output, width, height, refreshHz, scale, rotation)
	case "x11":
		if !commandAvailable("xrandr") {
			return fmt.Errorf("xrandr não está disponível")
		}
		return applyXrandr(s, output, width, height, refreshHz, scale, rotation)
	default:
		return fmt.Errorf("tipo de sessão desconhecido: %s", s.kind)
	}
}
