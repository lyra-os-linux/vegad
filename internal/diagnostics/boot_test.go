package diagnostics

import (
	"context"
	"strings"
	"testing"
)

// bootHost answers systemd-analyze from the workstation captures (30/10/2026)
// and "systemctl show" from props (unit → property → value); everything else
// is an unexpected command.
type bootHost struct {
	t     *testing.T
	out   map[string]string
	props map[string]map[string]string
	ran   []string
}

func newBootHost(t *testing.T) *bootHost {
	return &bootHost{t: t, out: map[string]string{
		"systemd-analyze time":                                               string(fixture(t, "analyze-time.txt")),
		"systemd-analyze blame --no-pager":                                   string(fixture(t, "analyze-blame.txt")),
		"systemd-analyze critical-chain graphical.target --no-pager":         string(fixture(t, "analyze-chain.txt")),
		"journalctl -b -o cat --no-pager -q -g Timed out waiting for device": "",
	}, props: map[string]map[string]string{
		// graphical.target at 4 s; the update check started 2 min later.
		"graphical.target":                   {"ActiveEnterTimestampMonotonic": "4000000"},
		"vegad-update-check.service":         {"InactiveExitTimestampMonotonic": "125000000"},
		"vegad-trusted-keys.service":         {"InactiveExitTimestampMonotonic": "2900000"},
		"NetworkManager-wait-online.service": {"InactiveExitTimestampMonotonic": "2100000"},
		"network-online.target":              {"WantedBy": "iscsi.service", "RequiredBy": ""},
	}}
}

func (b *bootHost) env() *Env {
	env := fakeEnv(b.t, nil, nil, "systemd-analyze")
	env.Run = func(_ context.Context, name string, args ...string) ([]byte, int, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		b.ran = append(b.ran, key)
		if out, ok := b.out[key]; ok {
			return []byte(out), 0, nil
		}
		if name == "systemctl" && len(args) == 5 && args[0] == "show" {
			return []byte(b.props[args[4]][args[2]] + "\n"), 0, nil
		}
		b.t.Errorf("unexpected command: %s", key)
		return nil, 1, nil
	}
	return env
}

func TestBootDurationsParse(t *testing.T) {
	for text, want := range map[string]float64{"1min 59.569s": 119.569, "639ms": 0.639, "2.082s": 2.082, "1h 2min": 3720} {
		if got, ok := parseSystemdDuration(text); !ok || got < want-1e-9 || got > want+1e-9 {
			t.Errorf("%s: %v", text, got)
		}
	}
	times, ok := parseAnalyzeTime(string(fixture(t, "analyze-time.txt")))
	if !ok || times.phases["firmware"] != 4.573 || times.phases["kernel"] != 0.639 || times.phases["userspace"] != 3.975 || times.total != 18.423 || times.graphical != 3.971 {
		t.Fatalf("%+v", times)
	}
	if _, ok := parseAnalyzeTime("Bootup is not yet finished (org.freedesktop.systemd1.Manager.FinishTimestampMonotonic=0).\n"); ok {
		t.Fatal("unfinished boot parsed")
	}
}

func TestBootOnTheWorkstationIsHealthyAndBlamesNoTimerJob(t *testing.T) {
	host := newBootHost(t)
	result := runBoot(context.Background(), host.env(), noProgress)
	if result.Verdict != Ok || len(result.Findings) != 0 {
		t.Fatalf("%s %+v", result.Verdict, result.Findings)
	}
	for _, unit := range result.Metrics["slowest"].([]unitTime) {
		if unit.Unit == "vegad-update-check.service" {
			t.Fatal("a timer job that ran after the login was blamed for the boot")
		}
	}
	// 18.423 s in total, with the login reached 4 ms before userspace ended.
	if !strings.Contains(result.Summary, "18.4") {
		t.Fatalf("summary %q", result.Summary)
	}
}

const slowChain = `The time when unit became active or started is printed after the "@" character.
The time the unit took to start is printed after the "+" character.

graphical.target @38.115s
` + "`" + `-multi-user.target @38.115s
  ` + "`" + `-remote-fs.target @38.110s
    ` + "`" + `-iscsi.service @8.002s +30.105s
      ` + "`" + `-network-online.target @8.001s
        ` + "`" + `-NetworkManager-wait-online.service @2.001s +30.000s
          ` + "`" + `-NetworkManager.service @1.800s +200ms
`

func TestBootWaitingForTheNetworkIsFoundButKeptWhenSomethingNeedsIt(t *testing.T) {
	host := newBootHost(t)
	host.out["systemd-analyze critical-chain graphical.target --no-pager"] = slowChain
	result := runBoot(context.Background(), host.env(), noProgress)
	wait := findingCodes(result.Findings)["boot.wait-online"]
	if wait.Severity != SeverityAttention || wait.Remedy != "" || !strings.Contains(wait.Raw, "iscsi.service") {
		t.Fatalf("iscsi needs the network, no remedy: %+v", wait)
	}
	host.props["network-online.target"] = map[string]string{"WantedBy": "NetworkManager-wait-online.service"}
	result = runBoot(context.Background(), host.env(), noProgress)
	if wait := findingCodes(result.Findings)["boot.wait-online"]; wait.Remedy != "disable-wait-online" {
		t.Fatalf("nothing needs the network: %+v", wait)
	}
}

func TestBootSlowServicesOfTheImageGetGuidanceOnly(t *testing.T) {
	host := newBootHost(t)
	host.out["systemd-analyze blame --no-pager"] = "     45.200s minha-sync.service\n     12.001s plymouth-quit-wait.service\n      1.000s cups.service\n"
	host.props["minha-sync.service"] = map[string]string{"InactiveExitTimestampMonotonic": "1000000", "UnitFileState": "enabled", "UnitFilePreset": "disabled"}
	host.props["plymouth-quit-wait.service"] = map[string]string{"InactiveExitTimestampMonotonic": "1000000", "UnitFileState": "static", "UnitFilePreset": "enabled"}
	host.props["cups.service"] = map[string]string{"InactiveExitTimestampMonotonic": "1000000"}
	result := runBoot(context.Background(), host.env(), noProgress)
	var mine, image *Finding
	for i := range result.Findings {
		finding := &result.Findings[i]
		if finding.Code != "boot.slow-service" {
			continue
		}
		if strings.Contains(finding.Raw, "minha-sync") {
			mine = finding
		} else if strings.Contains(finding.Raw, "plymouth") {
			image = finding
		}
	}
	if mine == nil || mine.Remedy != "disable-service" || strings.Join(mine.RemedyArgs, ",") != "minha-sync.service" {
		t.Fatalf("user service: %+v", result.Findings)
	}
	if image == nil || image.Remedy != "" {
		t.Fatalf("image service: %+v", result.Findings)
	}
}

func TestBootMissingDeviceAndSlowSystem(t *testing.T) {
	host := newBootHost(t)
	host.out["journalctl -b -o cat --no-pager -q -g Timed out waiting for device"] = "Timed out waiting for device /dev/disk/by-uuid/1234-ABCD.\nTimed out waiting for device /dev/disk/by-uuid/1234-ABCD.\n"
	host.out["systemd-analyze time"] = "Startup finished in 1.2s (kernel) + 3.0s (initrd) + 1min 35.000s (userspace) = 1min 39.200s \ngraphical.target reached after 1min 34.900s in userspace.\n"
	result := runBoot(context.Background(), host.env(), noProgress)
	codes := findingCodes(result.Findings)
	if codes["boot.missing-device"].Raw != "/dev/disk/by-uuid/1234-ABCD" || codes["boot.slow"].Severity != SeverityAttention {
		t.Fatalf("%+v", result.Findings)
	}
	if _, firmware := result.Metrics["phases"].(map[string]float64)["firmware"]; firmware {
		t.Fatal("a VM has no firmware phase")
	}
}

func TestBootRemediesCheckAgainBeforeDisabling(t *testing.T) {
	host := newBootHost(t)
	host.out["systemctl disable minha-sync.service"] = ""
	host.props["minha-sync.service"] = map[string]string{"UnitFileState": "enabled", "UnitFilePreset": "disabled"}
	host.props["sshd.service"] = map[string]string{"UnitFileState": "enabled", "UnitFilePreset": "enabled"}
	env := host.env()
	remedy, _ := LookupRemedy("disable-service")
	result := remedy.Run(context.Background(), env, []string{"sshd.service", "minha-sync.service", "--now"}, noProgress)
	if result.Verdict != Ok || strings.Join(result.Metrics["disabled"].([]string), ",") != "minha-sync.service" {
		t.Fatalf("%+v", result)
	}
	for _, line := range host.ran {
		if strings.Contains(line, "disable sshd") || strings.Contains(line, "--now") {
			t.Fatalf("touched %s", line)
		}
	}
	wait, _ := LookupRemedy("disable-wait-online")
	if result := wait.Run(context.Background(), env, nil, noProgress); result.Verdict != Unavailable {
		t.Fatalf("iscsi still needs the network: %+v", result)
	}
}
