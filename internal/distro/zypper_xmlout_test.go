package distro

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// zypperXMLFixture is a synthetic but format-accurate rendition of a
// `zypper --xmlout update` stream for two packages, "foo" and "foo-devel"
// — chosen specifically because "foo-devel" contains "foo" as a substring,
// so it exercises the longest-match preference in both the download
// (prefix on the RPM basename) and progress (free-text) matchers.
const zypperXMLFixture = `<?xml version="1.0"?>
<stream>
<message type="info">Refreshing service...</message>
<install-summary download-size="1000" space-usage-diff="2000" space-usage-installed="3000" space-usage-removed="0" packages-to-change="2" need-restart="false" need-reboot="false">
<to-upgrade>
<solvable status="other-version" kind="package" name="foo" edition="1.1-1" edition-old="1.0-1" arch="x86_64"/>
<solvable status="other-version" kind="package" name="foo-devel" edition="1.1-1" edition-old="1.0-1" arch="x86_64"/>
</to-upgrade>
</install-summary>
<download url="http://example.com/repo/foo-devel-1.1-1.x86_64.rpm" percent="10" rate="1000"/>
<download url="http://example.com/repo/foo-devel-1.1-1.x86_64.rpm" percent="50" rate="1000"/>
<download url="http://example.com/repo/foo-devel-1.1-1.x86_64.rpm" rate="1000" done="0"/>
<download url="http://example.com/repo/foo-1.1-1.x86_64.rpm" percent="70" rate="1000"/>
<download url="http://example.com/repo/foo-1.1-1.x86_64.rpm" rate="1000" done="0"/>
<progress id="1" name="Installing: foo-devel-1.1-1.x86_64" value="40"/>
<progress id="1" name="Installing: foo-devel-1.1-1.x86_64" done="0"/>
<progress id="2" name="Installing: foo-1.1-1.x86_64" value="60"/>
<progress id="2" name="Installing: foo-1.1-1.x86_64" done="0"/>
</stream>
`

type pkgEvent struct {
	pkg     string
	phase   PackagePhase
	percent uint32
}

type overallEvent struct {
	percent uint32
	message string
}

func collectZypperEvents(t *testing.T, xmlBody string) ([]pkgEvent, []overallEvent) {
	t.Helper()
	var pkgEvents []pkgEvent
	var overallEvents []overallEvent
	report := func(percent uint32, message string) {
		overallEvents = append(overallEvents, overallEvent{percent, message})
	}
	pkgReport := func(pkg string, phase PackagePhase, percent uint32) {
		pkgEvents = append(pkgEvents, pkgEvent{pkg, phase, percent})
	}
	if err := parseZypperXMLStream(strings.NewReader(xmlBody), report, pkgReport); err != nil {
		t.Fatalf("parseZypperXMLStream: %v", err)
	}
	return pkgEvents, overallEvents
}

func TestParseZypperXMLStreamDownloadLongestMatchWins(t *testing.T) {
	pkgEvents, _ := collectZypperEvents(t, zypperXMLFixture)

	var devel, plain []pkgEvent
	for _, e := range pkgEvents {
		if e.phase != PackagePhaseDownload {
			continue
		}
		switch e.pkg {
		case "foo-devel":
			devel = append(devel, e)
		case "foo":
			plain = append(plain, e)
		default:
			t.Errorf("unexpected package in download event: %+v", e)
		}
	}

	wantDevel := []uint32{10, 50, 100}
	if len(devel) != len(wantDevel) {
		t.Fatalf("foo-devel download events = %+v, want percents %v", devel, wantDevel)
	}
	for i, want := range wantDevel {
		if devel[i].percent != want {
			t.Errorf("foo-devel download[%d].percent = %d, want %d", i, devel[i].percent, want)
		}
	}

	wantPlain := []uint32{70, 100}
	if len(plain) != len(wantPlain) {
		t.Fatalf("foo download events = %+v, want percents %v", plain, wantPlain)
	}
	for i, want := range wantPlain {
		if plain[i].percent != want {
			t.Errorf("foo download[%d].percent = %d, want %d", i, plain[i].percent, want)
		}
	}
}

func TestParseZypperXMLStreamProgressLongestMatchWins(t *testing.T) {
	pkgEvents, _ := collectZypperEvents(t, zypperXMLFixture)

	var devel, plain []pkgEvent
	for _, e := range pkgEvents {
		if e.phase != PackagePhaseInstall {
			continue
		}
		switch e.pkg {
		case "foo-devel":
			devel = append(devel, e)
		case "foo":
			plain = append(plain, e)
		default:
			t.Errorf("unexpected package in progress event: %+v", e)
		}
	}

	if len(devel) != 2 || devel[0].percent != 40 || devel[1].percent != 100 {
		t.Errorf("foo-devel install events = %+v, want [40 100]", devel)
	}
	if len(plain) != 2 || plain[0].percent != 60 || plain[1].percent != 100 {
		t.Errorf("foo install events = %+v, want [60 100]", plain)
	}
}

func TestParseZypperXMLStreamOverallPercentMonotonicAndCapped(t *testing.T) {
	_, overallEvents := collectZypperEvents(t, zypperXMLFixture)

	if len(overallEvents) == 0 {
		t.Fatalf("expected at least one overall progress report")
	}
	var last uint32
	for i, e := range overallEvents {
		if e.percent < last {
			t.Errorf("overall[%d].percent = %d regressed below previous %d", i, e.percent, last)
		}
		if e.percent > 99 {
			t.Errorf("overall[%d].percent = %d, want <= 99 (100 is reserved for the subprocess wrapper)", i, e.percent)
		}
		last = e.percent
	}
	if last != 99 {
		t.Errorf("final overall percent = %d, want 99 once both packages finished both phases", last)
	}
}

func TestParseZypperXMLStreamDoneInvertedSemantics(t *testing.T) {
	const fixture = `<?xml version="1.0"?>
<stream>
<install-summary packages-to-change="1">
<to-upgrade>
<solvable status="other-version" kind="package" name="broken" edition="1.1-1" arch="x86_64"/>
</to-upgrade>
</install-summary>
<download url="http://example.com/repo/broken-1.1-1.x86_64.rpm" percent="30" rate="1000"/>
<download url="http://example.com/repo/broken-1.1-1.x86_64.rpm" rate="1000" done="1"/>
</stream>
`
	pkgEvents, _ := collectZypperEvents(t, fixture)
	for _, e := range pkgEvents {
		if e.percent == 100 {
			t.Errorf("done=\"1\" (zypper's error case) must not be reported as 100%%: %+v", e)
		}
	}
}

func TestParseZypperXMLStreamNoInstallSummaryDoesNotPanic(t *testing.T) {
	const fixture = `<?xml version="1.0"?>
<stream>
<message type="info">Nothing to do.</message>
</stream>
`
	pkgEvents, overallEvents := collectZypperEvents(t, fixture)
	if len(pkgEvents) != 0 {
		t.Errorf("expected no per-package events without install-summary, got %+v", pkgEvents)
	}
	for _, e := range overallEvents {
		if e.percent != 0 {
			t.Errorf("expected overall percent to stay 0 without a known packages-to-change total, got %d", e.percent)
		}
	}
}

func TestParseZypperXMLStreamMalformedXMLReturnsError(t *testing.T) {
	const malformed = `<?xml version="1.0"?>
<stream>
<message type="info">Truncated
`
	report := func(uint32, string) {}
	pkgReport := func(string, PackagePhase, uint32) {}
	if err := parseZypperXMLStream(strings.NewReader(malformed), report, pkgReport); err == nil {
		t.Fatalf("expected an error for malformed/truncated XML")
	}
}

func exitWith(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("/bin/sh", "-c", "exit "+strconv.Itoa(code)).Run()
	if err == nil {
		t.Fatalf("exit %d did not fail", code)
	}
	return err
}

// A transaction that applied but skipped an untrusted repository (106), or
// asks for a reboot (102) or a zypper restart (103), is a success; the
// dotnet-sdk install that reported "falhou" with status 106 had installed.
func TestZypperTransactionInformationalExitsSucceed(t *testing.T) {
	warned := ""
	previous := zypperWarnf
	zypperWarnf = func(format string, args ...any) { warned = fmt.Sprintf(format, args...) }
	defer func() { zypperWarnf = previous }()
	for _, code := range []int{102, 103, 106} {
		messages := zypperXMLMessages{Warnings: []string{"Skipping repository 'code' because of the above error."}}
		if err := zypperTransactionError(exitWith(t, code), "", messages); err != nil {
			t.Fatalf("status %d: %v", code, err)
		}
	}
	if !strings.Contains(warned, "Skipping repository 'code'") {
		t.Fatalf("skipped repositories were not logged: %q", warned)
	}
}

// With --xmlout zypper explains failures in <message type="error">, not on
// stderr; the error must carry that text instead of ending in an empty "—".
func TestZypperTransactionErrorUsesXMLMessages(t *testing.T) {
	stream := `<?xml version="1.0"?>
<stream>
<message type="info">Loading repository data...</message>
<message type="error">Problem: the installed rust1.98 conflicts with 'rust+rustc' provided by rustup</message>
<message type="warning">Skipping repository 'code' because of the above error.</message>
</stream>`
	var messages zypperXMLMessages
	if err := parseZypperXMLStreamMessages(strings.NewReader(stream), nil, nil, &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages.Errors) != 1 || len(messages.Warnings) != 1 {
		t.Fatalf("messages = %+v", messages)
	}
	err := zypperTransactionError(exitWith(t, 4), "  ", messages)
	if err == nil || !strings.Contains(err.Error(), "conflicts with 'rust+rustc'") {
		t.Fatalf("error = %v", err)
	}
	if err := zypperTransactionError(exitWith(t, 4), "lock held by PID 42", messages); err == nil || !strings.Contains(err.Error(), "lock held") {
		t.Fatalf("stderr must win when present: %v", err)
	}
	if err := zypperTransactionError(exitWith(t, 104), "", zypperXMLMessages{}); err == nil {
		t.Fatal("104 (package not found) must stay a failure")
	}
}
