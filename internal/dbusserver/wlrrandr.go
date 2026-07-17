package dbusserver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var wlrModeRe = regexp.MustCompile(`^(\d+)x(\d+) px, ([0-9.]+) Hz(.*)$`)

// parseWlrRandr turns `wlr-randr` output into DisplayOutputs. Only wlroots
// based compositors (Sway and similar) implement the wlr-output-management
// protocol this depends on — GNOME (Mutter) and KDE (KWin) on Wayland do
// not, callers should treat a missing binary as "unsupported compositor",
// not as a bug.
func parseWlrRandr(output string) []DisplayOutput {
	var outputs []DisplayOutput
	var current *DisplayOutput
	inModes := false
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimRight(rawLine, " \t")
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			if current != nil {
				outputs = append(outputs, *current)
			}
			name := strings.Fields(line)[0]
			current = &DisplayOutput{Name: name, Scale: 1.0, Rotation: "normal"}
			inModes = false
			continue
		}
		if current == nil {
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "Modes:":
			inModes = true
		case strings.HasPrefix(trimmed, "Enabled:"):
			current.Enabled = strings.Contains(trimmed, "yes")
			inModes = false
		case strings.HasPrefix(trimmed, "Transform:"):
			current.Rotation = wlrTransformToRotation(strings.TrimSpace(strings.TrimPrefix(trimmed, "Transform:")))
			inModes = false
		case strings.HasPrefix(trimmed, "Scale:"):
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "Scale:"))
			if scale, err := strconv.ParseFloat(value, 64); err == nil {
				current.Scale = scale
			}
			inModes = false
		case inModes:
			if mode, ok := parseWlrMode(trimmed); ok {
				current.Modes = append(current.Modes, mode)
			}
		default:
			inModes = false
		}
	}
	if current != nil {
		outputs = append(outputs, *current)
	}
	// wlr-randr has no "primary" concept — the first enabled output stands
	// in, matching how most wlroots compositors treat output order.
	for i := range outputs {
		if outputs[i].Enabled {
			outputs[i].Primary = true
			break
		}
	}
	return outputs
}

func parseWlrMode(line string) (DisplayMode, bool) {
	match := wlrModeRe.FindStringSubmatch(line)
	if match == nil {
		return DisplayMode{}, false
	}
	width, err1 := strconv.ParseUint(match[1], 10, 32)
	height, err2 := strconv.ParseUint(match[2], 10, 32)
	refresh, err3 := strconv.ParseFloat(match[3], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return DisplayMode{}, false
	}
	flags := match[4]
	return DisplayMode{
		Width:     uint32(width),
		Height:    uint32(height),
		RefreshHz: refresh,
		Current:   strings.Contains(flags, "current"),
		Preferred: strings.Contains(flags, "preferred"),
	}, true
}

func wlrTransformToRotation(transform string) string {
	switch transform {
	case "90":
		return "right"
	case "180":
		return "inverted"
	case "270":
		return "left"
	default:
		return "normal"
	}
}

func rotationToWlrTransform(rotation string) string {
	switch rotation {
	case "right":
		return "90"
	case "inverted":
		return "180"
	case "left":
		return "270"
	default:
		return "normal"
	}
}

func applyWlrRandr(session *graphicalSession, output string, width, height uint32, refreshHz, scale float64, rotation string) error {
	args := []string{"--output", output}
	if width > 0 && height > 0 {
		if refreshHz > 0 {
			args = append(args, "--mode", fmt.Sprintf("%dx%d@%.2fHz", width, height, refreshHz))
		} else {
			args = append(args, "--mode", fmt.Sprintf("%dx%d", width, height))
		}
	}
	if scale > 0 {
		args = append(args, "--scale", fmt.Sprintf("%.4g", scale))
	}
	if rotation != "" {
		args = append(args, "--transform", rotationToWlrTransform(rotation))
	}
	out, err := session.run("wlr-randr", args...)
	if err != nil {
		return fmt.Errorf("wlr-randr: %s", out)
	}
	return nil
}
