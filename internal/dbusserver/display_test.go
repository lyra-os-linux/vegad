package dbusserver

import "testing"

const sampleXrandrQuery = `Screen 0: minimum 320 x 200, current 1920 x 1080, maximum 16384 x 16384
eDP-1 connected primary 1920x1080+0+0 (normal left inverted right x axis y axis) 344mm x 194mm
   1920x1080     60.00*+  59.94    59.96    59.93
   1680x1050     59.95    59.88
HDMI-1 disconnected (normal left inverted right x axis y axis)
`

func TestParseXrandrKeepsOnlyConnectedOutputsWithModesAndFlags(t *testing.T) {
	outputs := parseXrandr(sampleXrandrQuery)
	if len(outputs) != 1 {
		t.Fatalf("expected 1 connected output, got %d", len(outputs))
	}
	out := outputs[0]
	if out.Name != "eDP-1" || !out.Enabled || !out.Primary || out.Rotation != "normal" {
		t.Fatalf("unexpected output: %+v", out)
	}
	if len(out.Modes) != 6 {
		t.Fatalf("expected 6 modes, got %d: %+v", len(out.Modes), out.Modes)
	}
	first := out.Modes[0]
	if first.Width != 1920 || first.Height != 1080 || first.RefreshHz != 60.00 || !first.Current || !first.Preferred {
		t.Fatalf("unexpected first mode: %+v", first)
	}
	last := out.Modes[len(out.Modes)-1]
	if last.Width != 1680 || last.Height != 1050 || last.Current || last.Preferred {
		t.Fatalf("unexpected last mode: %+v", last)
	}
}

func TestParseXrandrDetectsNonNormalRotation(t *testing.T) {
	const rotated = `Screen 0: minimum 320 x 200, current 1080 x 1920, maximum 16384 x 16384
eDP-1 connected primary 1080x1920+0+0 left (normal left inverted right x axis y axis) 344mm x 194mm
   1920x1080     60.00*+
`
	outputs := parseXrandr(rotated)
	if len(outputs) != 1 || outputs[0].Rotation != "left" {
		t.Fatalf("expected rotation 'left', got %+v", outputs)
	}
}

const sampleWlrRandr = `eDP-1 "Some Panel"
  Make: Some Vendor
  Model: Some Panel
  Serial: unknown
  Physical size: 344x194 mm
  Enabled: yes
  Modes:
    1920x1080 px, 60.000000 Hz (preferred, current)
    1680x1050 px, 59.954102 Hz
  Position: 0,0
  Transform: normal
  Scale: 1.500000
`

func TestParseWlrRandrReadsModesScaleAndTransform(t *testing.T) {
	outputs := parseWlrRandr(sampleWlrRandr)
	if len(outputs) != 1 {
		t.Fatalf("expected 1 output, got %d", len(outputs))
	}
	out := outputs[0]
	if out.Name != "eDP-1" || !out.Enabled || !out.Primary {
		t.Fatalf("unexpected output: %+v", out)
	}
	if out.Scale != 1.5 || out.Rotation != "normal" {
		t.Fatalf("unexpected scale/rotation: %+v", out)
	}
	if len(out.Modes) != 2 {
		t.Fatalf("expected 2 modes, got %d: %+v", len(out.Modes), out.Modes)
	}
	if !out.Modes[0].Current || !out.Modes[0].Preferred {
		t.Fatalf("expected first mode current+preferred: %+v", out.Modes[0])
	}
	if out.Modes[1].Current || out.Modes[1].Preferred {
		t.Fatalf("expected second mode plain: %+v", out.Modes[1])
	}
}

func TestRotationWlrTransformRoundTrip(t *testing.T) {
	for _, rotation := range []string{"normal", "left", "right", "inverted"} {
		transform := rotationToWlrTransform(rotation)
		if got := wlrTransformToRotation(transform); got != rotation {
			t.Fatalf("round trip failed for %q: transform=%q got=%q", rotation, transform, got)
		}
	}
}
