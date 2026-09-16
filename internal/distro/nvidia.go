package distro

// This is a deliberately narrow first-install/adoption transaction. It is not
// a general NVIDIA migration or repair engine. Never answer solver conflicts,
// remove packages, switch kernels or accept an unreviewed version/provider.
import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const NvidiaVersion = "610.57.04"
const NvidiaKMP = "nvidia-open-driver-G07-signed-cuda-kmp-default"
const NvidiaOBSURL = "https://download.opensuse.org/repositories/home:/rodrigosbrito:/nvidia/openSUSE_Leap_16.1/"
const NvidiaUpstreamURL = "https://developer.download.nvidia.com/compute/cuda/repos/suse16/x86_64/"

var nvidiaSources = []struct{ alias, url, key string }{
	{"lyra-nvidia", NvidiaOBSURL, "399218A6E088C4053F4533BE58097F767EDCA82E"},
	{"lyra-nvidia-upstream", NvidiaUpstreamURL, "CF65941A859D4B4E6870F188736A284B3A8B5622"},
	// Leap's key must already be trusted by the base OS; never import an
	// additional key for the OS implicitly through the NVIDIA installer.
	{"lyra-nvidia-oss", "https://download.opensuse.org/distribution/leap/16.1/repo/oss/", ""},
}

func nvidiaRepoDefinition(alias, url string) string {
	return fmt.Sprintf("[%s]\nname=%s\nenabled=1\nautorefresh=1\nbaseurl=%s\ntype=rpm-md\ngpgcheck=1\nrepo_gpgcheck=1\npkg_gpgcheck=1\n", alias, alias, url)
}

// Validate existing admin configuration without rewriting it. Refuse duplicate
// enabled NVIDIA channels, changed trust settings/URLs and disabled Lyra repos.
func ValidateNvidiaRepositories(directory string) error {
	files, err := filepath.Glob(filepath.Join(directory, "*.repo"))
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		sections, err := nvidiaRepoSections(string(data))
		if err != nil {
			return err
		}
		for alias, values := range sections {
			enabled := values["enabled"] != "0"
			expected := ""
			if alias == "lyra-nvidia" {
				expected = NvidiaOBSURL
			}
			if alias == "lyra-nvidia-upstream" {
				expected = NvidiaUpstreamURL
			}
			if expected != "" {
				if seen[alias] || values["enabled"] != "1" || values["baseurl"] != expected || values["gpgcheck"] != "1" || values["repo_gpgcheck"] != "1" || values["pkg_gpgcheck"] != "1" {
					return fmt.Errorf("NVIDIA repository policy differs: %s; preserve and review administrator configuration", alias)
				}
				if filepath.Base(file) != alias+".repo" {
					return fmt.Errorf("NVIDIA repository alias in unexpected file: %s", alias)
				}
				seen[alias] = true
			} else if enabled && (strings.Contains(strings.ToLower(values["baseurl"]), "nvidia") || strings.Contains(strings.ToLower(alias), "nvidia")) {
				return fmt.Errorf("competing NVIDIA repository enabled: %s", alias)
			}
		}
	}
	return nil
}

func nvidiaRepoSections(data string) (map[string]map[string]string, error) {
	sections := map[string]map[string]string{}
	name := ""
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name = strings.TrimSpace(line[1 : len(line)-1])
			if _, ok := sections[name]; ok {
				return nil, fmt.Errorf("duplicate repository section")
			}
			sections[name] = map[string]string{}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if name != "" && ok {
			sections[name][strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return sections, nil
}

// Sources/cache are private to this transaction, so failure before RPM commit
// cannot leave an enabled, unguarded NVIDIA repository on the running system.
func InstallNvidiaBundle(report ProgressFunc, beforeCommit func() error) error {
	root, err := os.MkdirTemp("", "vegad-nvidia-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	repoDir := filepath.Join(root, "repos")
	if err = os.Mkdir(repoDir, 0700); err != nil {
		return err
	}
	for _, source := range nvidiaSources {
		if err = os.WriteFile(filepath.Join(repoDir, source.alias+".repo"), []byte(nvidiaRepoDefinition(source.alias, source.url)), 0600); err != nil {
			return err
		}
	}
	base := []string{"--xmlout", "--reposd-dir", repoDir, "--cache-dir", filepath.Join(root, "cache")}
	for i, source := range nvidiaSources {
		report(uint32(10+i*10), source.alias)
		args := append(append([]string{}, base...), "refresh", "--", source.alias)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		cmd := nvidiaCommand(ctx, args...)
		approval := repoKeyApproval{Fingerprint: source.key, Identity: repoKeyIdentity{Alias: source.alias, Name: source.alias}}
		err := runApprovedKeyRefresh(cmd, source.alias, approval, func() error { return nil })
		cancel()
		if err != nil {
			return err
		}
	}
	report(40, "NVIDIA: checking solver plan")
	args := append(base, "--no-refresh", "install", "--no-recommends", "--download-in-advance", "--", "lyra-nvidia="+NvidiaVersion)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := nvidiaCommand(ctx, args...)
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	if err = cmd.Start(); err != nil {
		return err
	}
	parseErr := answerNvidiaInstall(output, input, beforeCommit, report)
	input.Close()
	if parseErr != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	waitErr := cmd.Wait()
	if parseErr != nil {
		return parseErr
	}
	if waitErr != nil {
		return fmt.Errorf("NVIDIA RPM transaction: %w", waitErr)
	}
	return nil
}

func nvidiaCommand(ctx context.Context, args ...string) *exec.Cmd {
	base := interactiveZypperCommand(args...)
	cmd := exec.CommandContext(ctx, base.Path, base.Args[1:]...)
	cmd.Env = commandEnvC()
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

type nvidiaSolvable struct {
	Name       string `xml:"name,attr"`
	Edition    string `xml:"edition,attr"`
	Arch       string `xml:"arch,attr"`
	Kind       string `xml:"type,attr"`
	Repository string `xml:"repository,attr"`
}
type nvidiaPlan struct {
	Count  int `xml:"packages-to-change,attr"`
	Groups []struct {
		XMLName  xml.Name
		Packages []nvidiaSolvable `xml:"solvable"`
	} `xml:",any"`
}

func validateNvidiaPlan(plan nvidiaPlan) error {
	if plan.Count < 1 {
		return errors.New("NVIDIA plan has no integration package")
	}
	seen := map[string]bool{}
	for _, group := range plan.Groups {
		if group.XMLName.Local != "to-install" {
			return fmt.Errorf("NVIDIA plan refused: %s", group.XMLName.Local)
		}
		for _, pkg := range group.Packages {
			if seen[pkg.Name] || pkg.Kind != "package" {
				return errors.New("NVIDIA plan contains duplicate/non-package entries")
			}
			seen[pkg.Name] = true
			expectedRepo := "lyra-nvidia-upstream"
			version := NvidiaVersion + "-"
			arch := "x86_64"
			switch pkg.Name {
			case "lyra-nvidia":
				expectedRepo = "lyra-nvidia"
				arch = "noarch"
			case NvidiaKMP:
				expectedRepo = "lyra-nvidia-oss"
				version = NvidiaVersion + "_k6.12.0_160100.4-"
			case "nvidia-open", "nvidia-common-G07":
			case "nvidia-compute-G07", "nvidia-compute-utils-G07", "nvidia-gl-G07", "nvidia-video-G07", "nvidia-modprobe", "nvidia-persistenced":
			default:
				return fmt.Errorf("NVIDIA plan includes an unqualified dependency: %s", pkg.Name)
			}
			if pkg.Repository != expectedRepo || pkg.Arch != arch || !strings.HasPrefix(pkg.Edition, version) {
				return fmt.Errorf("NVIDIA package source/version/architecture refused: %s %s %s (%s)", pkg.Name, pkg.Edition, pkg.Arch, pkg.Repository)
			}
		}
	}
	if len(seen) != plan.Count || !seen["lyra-nvidia"] {
		return errors.New("NVIDIA plan is incomplete")
	}
	return nil
}

// Approve the validated plan in the SAME zypper process while it owns the
// system lock. A dry-run followed by a separate install would have a race.
// Reject key/signature/EULA/file-conflict/solver questions at this stage.
func answerNvidiaInstall(reader io.Reader, writer io.Writer, beforeCommit func() error, report ProgressFunc) error {
	decoder := xml.NewDecoder(io.LimitReader(reader, 32<<20))
	validated, accepted, closed := false, false, false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			if !closed || !accepted {
				return errors.New("NVIDIA transaction did not complete the reviewed protocol")
			}
			return nil
		}
		if err != nil {
			return err
		}
		if end, ok := token.(xml.EndElement); ok && end.Name.Local == "stream" {
			closed = true
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "install-summary":
			if validated || accepted {
				return errors.New("NVIDIA plan changed after review")
			}
			var plan nvidiaPlan
			if err := decoder.DecodeElement(&plan, &start); err != nil {
				return err
			}
			if err := validateNvidiaPlan(plan); err != nil {
				return err
			}
			validated = true
		case "prompt":
			var prompt struct {
				ID      string `xml:"id,attr"`
				Options []struct {
					Value string `xml:"value,attr"`
				} `xml:"option"`
			}
			if err := decoder.DecodeElement(&prompt, &start); err != nil {
				return err
			}
			offered := false
			for _, option := range prompt.Options {
				offered = offered || option.Value == "y"
			}
			// Zypper PromptId::COMMIT = 0; never answer another prompt, even if it
			// happens to offer the same yes/no options.
			if prompt.ID != "0" || !validated || accepted || !offered {
				return fmt.Errorf("NVIDIA additional confirmation refused (prompt %s)", prompt.ID)
			}
			if err := beforeCommit(); err != nil {
				return err
			}
			if _, err := io.WriteString(writer, "y\n"); err != nil {
				return err
			}
			accepted = true
			report(50, "NVIDIA: installing reviewed RPMs")
		case "message":
			var message string
			if err := decoder.DecodeElement(&message, &start); err != nil {
				return err
			}
			if xmlAttr(start, "type") == "error" {
				return fmt.Errorf("NVIDIA: %s", strings.TrimSpace(message))
			}
		}
	}
}

// Called only after validating the installed guard and full stack. Never
// override an administrator's file, even if it appears between review/commit.
func EnableNvidiaOBS(directory string) error {
	if err := ValidateNvidiaRepositories(directory); err != nil {
		return err
	}
	path := filepath.Join(directory, "lyra-nvidia.repo")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, writeErr := io.WriteString(f, nvidiaRepoDefinition("lyra-nvidia", NvidiaOBSURL))
	if writeErr == nil {
		writeErr = f.Chmod(0644)
	} // vegad intentionally uses UMask=0077.
	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return writeErr
	}
	return closeErr
}

// Recovery for a failed RPM commit that wrote the new repository payload but
// did not register its guard. Only disable the exact package-owned definition;
// the caller must establish that this file did not exist before the operation.
func DisableUnprotectedNvidiaRepository(directory string) error {
	path := filepath.Join(directory, "lyra-nvidia-upstream.repo")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	sections, err := nvidiaRepoSections(string(data))
	if err != nil {
		return err
	}
	values, ok := sections["lyra-nvidia-upstream"]
	if !ok || len(sections) != 1 || values["baseurl"] != NvidiaUpstreamURL || values["enabled"] != "1" {
		return errors.New("failed-install repository changed; review manually")
	}
	// Only the managed payload's exact line is changed, preserving other data.
	original := string(data)
	disabled := strings.Replace(original, "\nenabled=1\n", "\nenabled=0\n", 1)
	if disabled == original {
		return errors.New("could not disable failed-install repository")
	}
	return os.WriteFile(path, []byte(disabled), 0644)
}
