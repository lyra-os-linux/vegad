package dbusserver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	xrandrGeometryRe  = regexp.MustCompile(`\d+x\d+\+\d+\+\d+`)
	xrandrRotationSet = map[string]bool{"left": true, "right": true, "inverted": true, "normal": true}
)

// parseXrandr turns `xrandr --query` output into DisplayOutputs, keeping
// only connected outputs (disconnected ones have no usable modes).
func parseXrandr(output string) []DisplayOutput {
	var outputs []DisplayOutput
	var current *DisplayOutput
	for _, line := range strings.Split(output, "\n") {
		if line == "" || strings.HasPrefix(line, "Screen") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			if current != nil {
				outputs = append(outputs, *current)
				current = nil
			}
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[1] != "connected" {
				continue
			}
			// The rotation word (if any) sits right after the geometry
			// token and before the "(...)" list of *supported* rotations —
			// that parenthetical always contains "left"/"right"/"inverted"
			// regardless of what's actually active, so it can't be used to
			// detect the current rotation.
			enabled := false
			rotation := "normal"
			for i, field := range fields {
				if !xrandrGeometryRe.MatchString(field) {
					continue
				}
				enabled = true
				if i+1 < len(fields) && xrandrRotationSet[fields[i+1]] {
					rotation = fields[i+1]
				}
				break
			}
			current = &DisplayOutput{
				Name:     fields[0],
				Enabled:  enabled,
				Primary:  strings.Contains(line, "primary"),
				Scale:    1.0,
				Rotation: rotation,
			}
			continue
		}
		if current == nil {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		dims := strings.SplitN(fields[0], "x", 2)
		if len(dims) != 2 {
			continue
		}
		width, err1 := strconv.ParseUint(dims[0], 10, 32)
		height, err2 := strconv.ParseUint(dims[1], 10, 32)
		if err1 != nil || err2 != nil {
			continue
		}
		for _, token := range fields[1:] {
			isCurrent := strings.Contains(token, "*")
			preferred := strings.Contains(token, "+")
			rate := strings.TrimRight(token, "*+")
			refresh, err := strconv.ParseFloat(rate, 64)
			if err != nil {
				continue
			}
			current.Modes = append(current.Modes, DisplayMode{
				Width:     uint32(width),
				Height:    uint32(height),
				RefreshHz: refresh,
				Current:   isCurrent,
				Preferred: preferred,
			})
		}
	}
	if current != nil {
		outputs = append(outputs, *current)
	}
	return outputs
}

func applyXrandr(session *graphicalSession, output string, width, height uint32, refreshHz, scale float64, rotation string) error {
	args := []string{"--output", output}
	if width > 0 && height > 0 {
		args = append(args, "--mode", fmt.Sprintf("%dx%d", width, height))
	}
	if refreshHz > 0 {
		args = append(args, "--rate", fmt.Sprintf("%.2f", refreshHz))
	}
	if scale > 0 {
		args = append(args, "--scale", fmt.Sprintf("%.4gx%.4g", scale, scale))
	}
	if rotation != "" {
		args = append(args, "--rotate", rotation)
	}
	out, err := session.run("xrandr", args...)
	if err != nil {
		return fmt.Errorf("xrandr: %s", out)
	}
	return nil
}
