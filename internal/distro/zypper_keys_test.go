package distro

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Optional integration uses only a disposable --root RPM database and a local
// libzypp signing fixture. No host repository or trusted key is modified.
func TestZypperKeyApprovalWithIsolatedRoot(t *testing.T) {
	repository := os.Getenv("VEGA_ZYPPER_TEST_REPO")
	if repository == "" {
		t.Skip("set VEGA_ZYPPER_TEST_REPO to a disposable signed rpm-md repository")
	}
	root := t.TempDir()
	for _, sub := range []string{"etc/zypp/repos.d", "var/lib/rpm", "usr/lib/sysimage/rpm", "var/cache/zypp/pubkeys", "var/log", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	config := "[fixture]\nname=Fixture\nenabled=1\nautorefresh=0\nbaseurl=file://" + repository + "\ntype=rpm-md\ngpgcheck=1\n"
	if err := os.WriteFile(filepath.Join(root, "etc/zypp/repos.d/fixture.repo"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	makeCommand := func(args ...string) *exec.Cmd {
		cmd := packageCommand("zypper", append([]string{"--root", root}, args...)...)
		cmd.Env = append(commandEnvC(), "ZYPP_LOGFILE="+filepath.Join(root, "zypper.log"))
		return cmd
	}
	makeInteractiveCommand := func() *exec.Cmd {
		cmd := interactiveZypperCommand("--root", root, "--xmlout", "refresh", "fixture")
		cmd.Env = append(commandEnvC(), "ZYPP_LOGFILE="+filepath.Join(root, "zypper.log"))
		return cmd
	}
	identityOutput, err := makeCommand("--xmlout", "repos", "--details").Output()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := parseRepoKeyIdentity(string(identityOutput), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	// Initial refresh obtains the fingerprint without importing it.
	out, _ := makeCommand("--non-interactive", "refresh", "fixture").CombinedOutput()
	key, ok := parseZypperUntrustedKey("fixture", string(out))
	if !ok {
		t.Fatalf("no untrusted key proposal: %s", out)
	}
	checkIdentity := func() error {
		data, err := makeCommand("--xmlout", "repos", "--details").Output()
		if err != nil {
			return err
		}
		current, err := parseRepoKeyIdentity(string(data), "fixture")
		if err != nil {
			return err
		}
		if current != identity {
			return errors.New("repository changed")
		}
		return nil
	}
	wrong := repoKeyApproval{Fingerprint: strings.Repeat("A", 40), Identity: identity}
	err = runApprovedKeyRefresh(makeInteractiveCommand(), "fixture", wrong, checkIdentity)
	var changed *UntrustedKeyError
	if !errors.As(err, &changed) || changed.KeyId != key.KeyId {
		t.Fatalf("key swap not rejected: %v", err)
	}
	queryKeys := func() string {
		cmd := exec.Command("rpm", "--root", root, "--dbpath", "/var/lib/rpm", "-qa", "gpg-pubkey*", "--qf", "%{VERSION}\n")
		data, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.ToUpper(string(data))
	}
	shortID := key.KeyId[len(key.KeyId)-8:]
	if strings.Contains(queryKeys(), shortID) {
		t.Fatal("unapproved key imported")
	}
	approved := repoKeyApproval{Fingerprint: key.KeyId, Identity: identity}
	// Metadata in the signing fixture may intentionally reference missing
	// payloads; that must not affect the key-approval assertion.
	err = runApprovedKeyRefresh(makeInteractiveCommand(), "fixture", approved, checkIdentity)
	if !strings.Contains(queryKeys(), shortID) {
		t.Fatalf("approved key was not imported: %v", err)
	}
	t.Logf("approved fingerprint %s imported only into %s; refresh result: %v", key.KeyId, root, err)
}

const testApprovedFingerprint = "0123456789ABCDEF0123456789ABCDEF01234567"

func keyPromptXML(fingerprint, repository string) string {
	return fmt.Sprintf(`<gpgkey-info><repository>%s</repository><key-name>Fixture</key-name><key-fingerprint>%s</key-fingerprint></gpgkey-info><prompt id="14"><description>Trust key</description><option value="r"/><option value="t"/><option value="a"/></prompt>`, repository, fingerprint)
}

func TestRepoKeyApprovalAcceptsOnlyFullFingerprint(t *testing.T) {
	for _, value := range []string{"", "01234567", "ABCDEF01234567", strings.Repeat("X", 40), "--all"} {
		if _, err := normalizeKeyFingerprint(value); err == nil {
			t.Fatalf("accepted incomplete/invalid fingerprint %q", value)
		}
	}
	got, err := normalizeKeyFingerprint("0123 4567 89ab cdef 0123 4567 89ab cdef 0123 4567")
	if err != nil || got != testApprovedFingerprint {
		t.Fatalf("full fingerprint = %q %v", got, err)
	}
}

func TestRepoKeyPromptBindsKeyRepositoryAndSingleApproval(t *testing.T) {
	approval := repoKeyApproval{Fingerprint: testApprovedFingerprint, Identity: repoKeyIdentity{Alias: "fixture", Name: "Fixture Repo"}}
	for _, test := range []struct {
		name, xml, answer string
		changed           bool
		wantError         bool
	}{
		{"approved", keyPromptXML(testApprovedFingerprint, "Fixture Repo"), "a\n", false, false},
		{"switched-key", keyPromptXML(strings.Repeat("A", 40), "Fixture Repo"), "", false, true},
		{"other-repo", keyPromptXML(testApprovedFingerprint, "Other Repo"), "", false, true},
		{"changed-url", keyPromptXML(testApprovedFingerprint, "Fixture Repo"), "", true, true},
		{"additional-key", keyPromptXML(testApprovedFingerprint, "Fixture Repo") + keyPromptXML(strings.Repeat("A", 40), "Fixture Repo"), "a\n", false, true},
		{"unsigned", `<prompt id="11"><option value="yes"/></prompt>`, "", false, true},
		{"unknown-key", `<prompt id="13"><option value="yes"/></prompt>`, "", false, true},
		{"bad-signature", `<prompt id="15"><option value="yes"/></prompt>`, "", false, true},
		{"missing-key", `<prompt id="14"><option value="a"/></prompt>`, "", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var answer bytes.Buffer
			err := answerApprovedKeyPrompts(strings.NewReader("<stream>"+test.xml+"</stream>"), &answer, "fixture", approval, func() error {
				if test.changed {
					return errors.New("repository URL changed")
				}
				return nil
			})
			if (err != nil) != test.wantError || answer.String() != test.answer {
				t.Fatalf("answer=%q err=%v", answer.String(), err)
			}
		})
	}
}

func TestRepoKeyPromptReadsEscapedKeyInfoAndRejectsTruncatedOutput(t *testing.T) {
	approval := repoKeyApproval{Fingerprint: testApprovedFingerprint, Identity: repoKeyIdentity{Alias: "fixture"}}
	info := fmt.Sprintf(`&lt;gpgkey-info&gt;&lt;repository&gt;fixture&lt;/repository&gt;&lt;key-fingerprint&gt;%s&lt;/key-fingerprint&gt;&lt;/gpgkey-info&gt;`, testApprovedFingerprint)
	input := `<stream><message type="info">` + info + `</message><prompt id="14"><option value="a"/></prompt></stream>`
	var answer bytes.Buffer
	if err := answerApprovedKeyPrompts(strings.NewReader(input), &answer, "fixture", approval, func() error { return nil }); err != nil || answer.String() != "a\n" {
		t.Fatalf("escaped XML: %q %v", answer.String(), err)
	}
	for _, data := range []string{"", "<stream>", "<stream><prompt"} {
		if err := answerApprovedKeyPrompts(strings.NewReader(data), &answer, "fixture", approval, func() error { return nil }); err == nil {
			t.Fatalf("truncated XML accepted: %q", data)
		}
	}
}

func TestRepoKeyIdentityChangesWithURLAndSecurityPolicy(t *testing.T) {
	input := `<stream><repo-list><repo alias="fixture" name="Fixture" gpgcheck="1"><url>https://example.invalid/one</url></repo></repo-list></stream>`
	first, err := parseRepoKeyIdentity(input, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{strings.ReplaceAll(input, "/one", "/two"), strings.ReplaceAll(input, `gpgcheck="1"`, `gpgcheck="0"`)} {
		other, err := parseRepoKeyIdentity(changed, "fixture")
		if err != nil || first == other {
			t.Fatalf("changed repository reused identity: %v", err)
		}
	}
	if _, err := parseRepoKeyIdentity(input, "missing"); err == nil {
		t.Fatal("unknown alias accepted")
	}
}
