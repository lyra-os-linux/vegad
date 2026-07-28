package distro

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
)

// knownPackages tracks the package names seen in a zypper --xmlout stream's
// <install-summary>/<solvable name="..."> block, kept sorted by name length
// descending so match() prefers "foo-devel" over "foo" when both could
// plausibly match the same download URL or progress text.
type knownPackages struct {
	names []string
	seen  map[string]bool
}

func (k *knownPackages) add(name string) {
	if name == "" || k.seen[name] {
		return
	}
	if k.seen == nil {
		k.seen = map[string]bool{}
	}
	k.seen[name] = true
	k.names = append(k.names, name)
	sort.Slice(k.names, func(i, j int) bool { return len(k.names[i]) > len(k.names[j]) })
}

// match returns the longest known package name for which fn(name, text)
// reports a match, or ok=false if none do.
func (k *knownPackages) match(text string, fn func(name, text string) bool) (name string, ok bool) {
	for _, candidate := range k.names {
		if fn(candidate, text) {
			return candidate, true
		}
	}
	return "", false
}

// rpmBasenameHasPrefix matches a <download url="..."> against a package
// name using the standard RPM filename convention name-version-release.arch.rpm.
func rpmBasenameHasPrefix(name, basename string) bool {
	return strings.HasPrefix(basename, name+"-")
}

// textContainsName matches a <progress name="..."> free-text label against
// a package name — zypper's install/commit step doesn't expose a
// structured package identity the way <download url> does, only this
// human-readable string.
func textContainsName(name, text string) bool {
	return strings.Contains(text, name)
}

// xmlProgressState accumulates cross-element state while streaming a
// zypper --xmlout transaction: the known package set, how many packages
// were declared up front, and how many have finished each phase so far —
// enough to compute a real overall percent instead of exec.go's synthetic
// per-line heuristic.
type xmlProgressState struct {
	known            knownPackages
	packagesToChange int
	downloadsDone    int
	installsDone     int
	lastOverall      uint32
	lastMessage      string
}

// overall computes a monotonically increasing 0-99 percent from real
// completed-phase counts (download and install are each worth half the
// bar). 100 is reserved for the subprocess wrapper's own final call once
// the process has actually exited, matching the precedent set by
// runStreamingCmd's coarse progress.
func (s *xmlProgressState) overall() uint32 {
	if s.packagesToChange <= 0 {
		return s.lastOverall
	}
	v := uint32((s.downloadsDone + s.installsDone) * 100 / (2 * s.packagesToChange))
	if v < s.lastOverall {
		v = s.lastOverall
	}
	if v > 99 {
		v = 99
	}
	s.lastOverall = v
	return v
}

func xmlAttr(t xml.StartElement, local string) string {
	for _, a := range t.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// parseZypperXMLStream reads a zypper --xmlout stream (see
// /usr/share/zypper/xml/xmlout.rnc) as it arrives, translating <download>
// and <progress> elements into per-package pkgReport calls and a real
// overall percent fed to report — replacing the "+5 per line" guess
// exec.go's runStreamingCmd falls back to for non-XML output.
//
// zypper's done attribute is inverted from the intuitive reading:
// done="0" means the step succeeded, done="1" means it errored — a failed
// step is simply not counted as progress, but doesn't abort parsing (the
// process's own exit code remains the authoritative success/failure
// signal, checked by the caller after Wait()).
func parseZypperXMLStream(r io.Reader, report ProgressFunc, pkgReport PackageProgressFunc) error {
	if report == nil {
		report = func(uint32, string) {}
	}
	if pkgReport == nil {
		pkgReport = func(string, PackagePhase, uint32) {}
	}

	state := &xmlProgressState{}
	dec := xml.NewDecoder(r)
	inMessage := false
	var messageText strings.Builder

	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("saída malformada: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "install-summary":
				if v := xmlAttr(t, "packages-to-change"); v != "" {
					if n, err := strconv.Atoi(v); err == nil {
						state.packagesToChange = n
					}
				}
			case "solvable":
				state.known.add(xmlAttr(t, "name"))
			case "download":
				handleZypperDownload(t, state, report, pkgReport)
			case "progress":
				handleZypperProgress(t, state, report, pkgReport)
			case "message":
				inMessage = true
				messageText.Reset()
			}
		case xml.CharData:
			if inMessage {
				messageText.Write(t)
			}
		case xml.EndElement:
			if t.Name.Local == "message" {
				inMessage = false
				if text := strings.TrimSpace(messageText.String()); text != "" {
					state.lastMessage = text
					report(state.overall(), text)
				}
			}
		}
	}
}

func handleZypperDownload(t xml.StartElement, state *xmlProgressState, report ProgressFunc, pkgReport PackageProgressFunc) {
	name, ok := state.known.match(path.Base(xmlAttr(t, "url")), rpmBasenameHasPrefix)
	if done := xmlAttr(t, "done"); done != "" {
		if done == "0" {
			state.downloadsDone++
			if ok {
				pkgReport(name, PackagePhaseDownload, 100)
			}
			report(state.overall(), state.lastMessage)
		}
		return
	}
	if !ok {
		return
	}
	if v := xmlAttr(t, "percent"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			pkgReport(name, PackagePhaseDownload, uint32(p))
		}
	}
}

func handleZypperProgress(t xml.StartElement, state *xmlProgressState, report ProgressFunc, pkgReport PackageProgressFunc) {
	name, ok := state.known.match(xmlAttr(t, "name"), textContainsName)
	if done := xmlAttr(t, "done"); done != "" {
		if done == "0" {
			state.installsDone++
			if ok {
				pkgReport(name, PackagePhaseInstall, 100)
			}
			report(state.overall(), state.lastMessage)
		}
		return
	}
	if !ok {
		return
	}
	if v := xmlAttr(t, "value"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			pkgReport(name, PackagePhaseInstall, uint32(p))
		}
	}
}

// runZypperTransactionXML runs zypper --xmlout <args...>, streaming its
// output through parseZypperXMLStream for real per-package progress
// instead of runStreamingCommand's coarse per-line guess. Unlike
// runStreamingCmd, stderr is kept separate from stdout — --xmlout needs a
// clean XML channel — and only surfaced in the error path.
func runZypperTransactionXML(args []string, report ProgressFunc, pkgReport PackageProgressFunc, startMsg, doneMsg string) error {
	report(0, startMsg)

	cmd := exec.Command("zypper", append([]string{"--xmlout"}, args...)...)
	cmd.Env = commandEnvC()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return err
	}

	parseErr := parseZypperXMLStream(stdout, report, pkgReport)
	waitErr := cmd.Wait()

	if waitErr != nil {
		return fmt.Errorf("falha ao processar pacotes: %w — %s", waitErr, strings.TrimSpace(stderrBuf.String()))
	}
	if parseErr != nil {
		return fmt.Errorf("falha ao processar pacotes: %w", parseErr)
	}

	report(100, doneMsg)
	return nil
}
