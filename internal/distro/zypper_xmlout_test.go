package distro

import (
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
