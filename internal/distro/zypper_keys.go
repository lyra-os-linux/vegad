package distro

import (
	"context"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var fullFingerprintPattern = regexp.MustCompile(`^(?:[0-9A-F]{40}|[0-9A-F]{64})$`)

func normalizeKeyFingerprint(value string) (string, error) {
	value = strings.ToUpper(strings.Join(strings.Fields(value), ""))
	if !fullFingerprintPattern.MatchString(value) {
		return "", fmt.Errorf("a aprovação exige o fingerprint completo da chave")
	}
	return value, nil
}

type repoKeyIdentity struct {
	Digest [32]byte
	Alias  string
	Name   string
}

type repoKeyApproval struct {
	Fingerprint string
	Identity    repoKeyIdentity
}

func repoKeyOutput(args ...string) (string, error) {
	cmd := packageCommand("zypper", args...)
	cmd.Env = commandEnvC()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func parseRepoKeyIdentity(data, alias string) (repoKeyIdentity, error) {
	decoder := xml.NewDecoder(strings.NewReader(data))
	for {
		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return repoKeyIdentity{}, fmt.Errorf("repositório %q não encontrado", alias)
			}
			return repoKeyIdentity{}, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "repo" {
			continue
		}
		var found, name string
		var identityAttributes []xml.Attr
		for _, attr := range start.Attr {
			// A new repository's format can be detected by the first refresh.
			// That does not change its source or signature-checking policy.
			if attr.Name.Local != "type" {
				identityAttributes = append(identityAttributes, attr)
			}
			switch attr.Name.Local {
			case "alias":
				found = attr.Value
			case "name":
				name = attr.Value
			}
		}
		var body struct {
			Inner string `xml:",innerxml"`
		}
		if err := decoder.DecodeElement(&body, &start); err != nil {
			return repoKeyIdentity{}, err
		}
		if found == alias {
			// Includes resolved URLs and configuration attributes, without
			// exposing URLs (which may contain credentials) in error messages.
			return repoKeyIdentity{sha256.Sum256([]byte(fmt.Sprint(identityAttributes) + body.Inner)), alias, name}, nil
		}
	}
}

func readRepoKeyIdentity(repo string) (repoKeyIdentity, error) {
	out, err := repoKeyOutput("--xmlout", "--non-interactive", "repos", "--details")
	if err != nil {
		return repoKeyIdentity{}, fmt.Errorf("consultar identidade do repositório: %w", err)
	}
	return parseRepoKeyIdentity(out, repo)
}

// Caller holds keyMu. Rejections are proposals only, never imports.
func (z *zypperBackend) proposeRepoKey(repo string) error {
	before, err := readRepoKeyIdentity(repo)
	if err != nil {
		return err
	}
	out, refreshErr := repoKeyOutput("--non-interactive", "refresh", "--", repo)
	if refreshErr == nil {
		delete(z.pendingKeys, repo)
		return nil
	}
	key, ok := parseZypperUntrustedKey(repo, out)
	if !ok {
		return fmt.Errorf("atualizar repositório: %w — %s", refreshErr, strings.TrimSpace(out))
	}
	after, err := readRepoKeyIdentity(repo)
	if err != nil || before != after {
		return fmt.Errorf("repositório mudou durante a consulta da chave; repita a operação")
	}
	if z.pendingKeys == nil {
		z.pendingKeys = make(map[string]repoKeyApproval)
	}
	z.pendingKeys[repo] = repoKeyApproval{key.KeyId, before}
	return key
}

type zypperKeyInfo struct {
	Repository  string `xml:"repository"`
	Name        string `xml:"key-name"`
	Fingerprint string `xml:"key-fingerprint"`
}

// Zypper emits structured gpgkey-info, sometimes XML-escaped inside a message.
// Only prompt 14 (GPG_KEY_TRUST) with an exact full fingerprint may get "a".
// Every other prompt, malformed input, substituted key or second key aborts.
func answerApprovedKeyPrompts(reader io.Reader, writer io.Writer, repo string, approval repoKeyApproval, checkIdentity func() error) error {
	decoder := xml.NewDecoder(io.LimitReader(reader, 8<<20))
	var key *zypperKeyInfo
	accepted := false
	closed := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			if !closed {
				return fmt.Errorf("resposta XML incompleta do Zypper")
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
		case "gpgkey-info":
			key = new(zypperKeyInfo)
			if err := decoder.DecodeElement(key, &start); err != nil {
				return err
			}
		case "message":
			var message string
			if err := decoder.DecodeElement(&message, &start); err != nil {
				return err
			}
			if at := strings.Index(message, "<gpgkey-info>"); at >= 0 {
				end := strings.Index(message[at:], "</gpgkey-info>")
				if end < 0 {
					return fmt.Errorf("descrição XML da chave incompleta")
				}
				key = new(zypperKeyInfo)
				if err := xml.Unmarshal([]byte(message[at:at+end+len("</gpgkey-info>")]), key); err != nil {
					return err
				}
			}
		case "prompt":
			var prompt struct {
				ID      int `xml:"id,attr"`
				Options []struct {
					Value string `xml:"value,attr"`
				} `xml:"option"`
			}
			if err := decoder.DecodeElement(&prompt, &start); err != nil {
				return err
			}
			if prompt.ID != 14 || key == nil || accepted {
				return fmt.Errorf("Zypper solicitou confirmação adicional não aprovada")
			}
			fingerprint, err := normalizeKeyFingerprint(key.Fingerprint)
			if err != nil {
				return err
			}
			if fingerprint != approval.Fingerprint {
				return &UntrustedKeyError{Repo: repo, KeyId: fingerprint, Fingerprint: key.Fingerprint, UserId: key.Name}
			}
			if key.Repository != approval.Identity.Name && key.Repository != approval.Identity.Alias {
				return fmt.Errorf("a chave apresentada pertence a outro repositório")
			}
			canImport := false
			for _, option := range prompt.Options {
				canImport = canImport || option.Value == "a"
			}
			if !canImport {
				return fmt.Errorf("Zypper não ofereceu importação da chave")
			}
			if err := checkIdentity(); err != nil {
				return err
			}
			if _, err := io.WriteString(writer, "a\n"); err != nil {
				return err
			}
			accepted = true
			key = nil
		}
	}
}

func refreshWithApprovedKey(repo string, approval repoKeyApproval) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	base := interactiveZypperCommand("--xmlout", "refresh", "--", repo)
	cmd := exec.CommandContext(ctx, base.Path, base.Args[1:]...)
	cmd.Env = commandEnvC()
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	return runApprovedKeyRefresh(cmd, repo, approval, func() error {
		current, err := readRepoKeyIdentity(repo)
		if err != nil {
			return err
		}
		if current != approval.Identity {
			return fmt.Errorf("repositório mudou antes da importação; revise a chave novamente")
		}
		return nil
	})
}

// Zypper reads confirmations from /dev/tty, not stdin. util-linux script gives
// it a private controlling terminal; --echo=never keeps replies out of XML.
// Each argument is shell-quoted separately because script's API takes a command
// string. util-linux is already a required dependency of vegad.
func interactiveZypperCommand(args ...string) *exec.Cmd {
	words := append([]string{"/usr/bin/zypper"}, args...)
	for i, word := range words {
		words[i] = "'" + strings.ReplaceAll(word, "'", "'\\''") + "'"
	}
	return exec.Command("script", "--quiet", "--return", "--flush", "--echo=never", "--command", "umask 0022; exec "+strings.Join(words, " "), "/dev/null")
}

func runApprovedKeyRefresh(cmd *exec.Cmd, repo string, approval repoKeyApproval, checkIdentity func() error) error {
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	if err := cmd.Start(); err != nil {
		return err
	}
	parseErr := answerApprovedKeyPrompts(output, input, repo, approval, checkIdentity)
	input.Close()
	if parseErr != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	waitErr := cmd.Wait()
	if parseErr != nil {
		return parseErr
	}
	if waitErr != nil {
		return fmt.Errorf("Zypper recusou a atualização após conferir a chave: %w", waitErr)
	}
	return nil
}
