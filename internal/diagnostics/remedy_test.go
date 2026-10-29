package diagnostics

import (
	"context"
	"strings"
	"testing"
)

func TestChangedBinaryProposesReinstallingItsPackage(t *testing.T) {
	rpm := "S.5....T.    /usr/lib/vega/vegad\nmissing     /usr/lib64/libfoo.so.1\n"
	env := fakeEnv(t, map[string]call{
		"rpm -Va": {out: []byte(rpm), code: 1},
		"rpm -qf --qf %{NAME}\n /usr/lib/vega/vegad /usr/lib64/libfoo.so.1": {out: []byte("vegad\nlibfoo1\n")},
		"zypper -n verify --dry-run":                                        {out: fixture(t, "zypper-verify.txt")},
	}, nil, "rpm", "zypper")
	result := runPackages(context.Background(), env, noProgress)
	for _, code := range []string{"packages.binary-changed", "packages.missing"} {
		f := findingCodes(result.Findings)[code]
		if f.Remedy != "reinstall-packages" || strings.Join(f.RemedyArgs, ",") != "vegad,libfoo1" {
			t.Fatalf("%s: %+v", code, f)
		}
	}
	if f := findingCodes(result.Findings)["packages.config-changed"]; f.Remedy != "" {
		t.Fatal("customized configurations are never reverted")
	}
}

func TestOnlyUnitsAndDisabledNTPGetRemedies(t *testing.T) {
	result := runLogs(context.Background(), logsEnv(t, `[{"unit":"vegad-trusted-keys.service","description":"x"},{"unit":"--evil","description":"y"}]`), noProgress)
	units := findingCodes(result.Findings)["logs.failed-units"]
	if units.Remedy != "restart-units" || strings.Join(units.RemedyArgs, ",") != "vegad-trusted-keys.service" {
		t.Fatalf("units %+v", units)
	}
	if f := findingCodes(result.Findings)["logs.coredumps"]; f.Remedy != "" {
		t.Fatal("crashes have no automatic fix")
	}
	cases := []struct{ ntp, synced, code, remedy string }{
		{"no", "yes", "platform.ntp-off", "enable-ntp"}, // off but still right: drifts from now on
		{"no", "no", "platform.ntp-off", "enable-ntp"},
		{"yes", "no", "platform.ntp", ""}, // on but unanswered: network
		{"yes", "yes", "", ""},
	}
	for _, c := range cases {
		env := fakeEnv(t, map[string]call{"timedatectl show": {out: []byte("NTP=" + c.ntp + "\nNTPSynchronized=" + c.synced + "\n")}}, platformFiles(), "timedatectl")
		codes := findingCodes(runPlatform(context.Background(), env, noProgress).Findings)
		_, off := codes["platform.ntp-off"]
		_, unsynced := codes["platform.ntp"]
		got := ""
		if off {
			got = "platform.ntp-off"
		} else if unsynced {
			got = "platform.ntp"
		}
		if got != c.code || codes[c.code].Remedy != c.remedy {
			t.Errorf("NTP=%s synced=%s: %q remedy %q", c.ntp, c.synced, got, codes[c.code].Remedy)
		}
	}
}

func TestRemediesRunOnlyValidatedArguments(t *testing.T) {
	env := fakeEnv(t, map[string]call{
		"zypper -n --no-refresh install -f vegad libfoo1": {out: []byte("done")},
	}, nil)
	reinstall, _ := LookupRemedy("reinstall-packages")
	result := reinstall.Run(context.Background(), env, []string{"vegad", "--from=evil", "../x", "libfoo1"}, noProgress)
	if result.Verdict != Ok || result.ID != "repair:reinstall-packages" {
		t.Fatalf("%+v", result)
	}
	env = fakeEnv(t, map[string]call{"zypper -n --no-refresh install -f vegad": {out: []byte("Problem: nothing provides x"), code: 4}}, nil)
	if result := reinstall.Run(context.Background(), env, []string{"vegad"}, noProgress); result.Verdict != Fail || findingCodes(result.Findings)["repair.failed"].Code == "" {
		t.Fatalf("zypper failure: %+v", result)
	}
	if result := reinstall.Run(context.Background(), env, []string{"-x"}, noProgress); result.Verdict != Unavailable {
		t.Fatalf("no valid package: %+v", result)
	}
}

func TestRestartUnitsReportsUnitsThatStillFail(t *testing.T) {
	env := fakeEnv(t, map[string]call{
		"systemctl reset-failed -- a.service": {},
		"systemctl restart -- a.service":      {},
		"systemctl is-failed -- a.service":    {out: []byte("active\n"), code: 1},
		"systemctl reset-failed -- b.service": {},
		"systemctl restart -- b.service":      {out: []byte("Job failed"), code: 1},
		"systemctl is-failed -- b.service":    {out: []byte("failed\n")},
	}, nil)
	restart, _ := LookupRemedy("restart-units")
	result := restart.Run(context.Background(), env, []string{"a.service", "b.service"}, noProgress)
	if result.Verdict != Fail || strings.Join(result.Metrics["restarted"].([]string), ",") != "a.service" {
		t.Fatalf("%+v", result)
	}
	if !strings.Contains(findingCodes(result.Findings)["repair.unit-failed"].Text, "b.service") {
		t.Fatalf("%+v", result.Findings)
	}
}

func TestEnableNTP(t *testing.T) {
	env := fakeEnv(t, map[string]call{"timedatectl set-ntp true": {}}, nil)
	ntp, _ := LookupRemedy("enable-ntp")
	if result := ntp.Run(context.Background(), env, nil, noProgress); result.Verdict != Ok {
		t.Fatalf("%+v", result)
	}
	if _, ok := LookupRemedy("format-disk"); ok {
		t.Fatal("unknown remedy accepted")
	}
}
