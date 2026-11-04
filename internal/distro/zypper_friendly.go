package distro

import (
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path"
	"regexp"
	"strings"
)

// Zypper exit statuses with a meaning the user can act on (zypper(8),
// EXIT CODES). They do not depend on the locale, unlike zypper's text.
const (
	zypperExitZyppError      = 4
	zypperExitLockedStatus   = 7
	zypperExitCommitError    = 8
	zypperExitNotFound       = 104
	zypperExitScriptFailed   = 107
	zypperFriendlyLogMaxRune = 2000
)

// ZypperError is a zypper failure explained in words the user can act on.
// Error() is what Vega shows; zypper's own explanation goes to the log, and
// the exit status stays reachable through Unwrap.
type ZypperError struct {
	// Kind is one of: locked, download, repository, conflict, not-found,
	// disk-space, signature, script.
	Kind    string
	Message string
	err     error
}

func (e *ZypperError) Error() string { return e.Message }
func (e *ZypperError) Unwrap() error { return e.err }

// explainZypperError returns err as a *ZypperError when detail (zypper's
// output) and the exit status show a failure the user can act on, logging
// zypper's text. Any other failure is returned unchanged, so nothing that
// was diagnosable before loses its detail.
func explainZypperError(err error, detail string) error {
	if err == nil {
		return nil
	}
	var already *ZypperError
	if errors.As(err, &already) {
		return err
	}
	friendly := classifyZypperFailure(err, detail)
	if friendly == nil {
		return err
	}
	friendly.err = err
	zypperWarnf("vegad: zypper (%s): %v — %s", friendly.Kind, err, truncateRunes(oneLine(detail), zypperFriendlyLogMaxRune))
	return friendly
}

// zypperFailureText matches English libzypp/zypper texts (LC_ALL=C).
var (
	zypperLockedTexts = []string{"System management is locked"}
	// A package (*.rpm) that could not be retrieved; repository metadata
	// failures are told apart by their URL.
	zypperDownloadTexts = []string{"Download (curl) error", "Curl error", "Failed to provide Package", "Timeout exceeded when accessing"}
	zypperNetworkTexts  = []string{"could not resolve host", "couldn't resolve host", "couldn't connect to server", "failed to connect", "network is unreachable", "connection timed out", "timeout was reached", "connection reset"}
	zypperRepoTexts     = []string{"Valid metadata not found at specified URL", "Repository type can't be determined", "is invalid.", "Problem retrieving files from"}
	zypperSpaceTexts    = []string{"Not enough disk space", "No space left on device", "insufficient disk space", "not enough free space"}
	zypperSignTexts     = []string{"Signature verification failed", "Digest verification failed", "is not signed", "NOKEY", "checksum does not match", "File checksum is wrong"}
	zypperNotFoundTexts = []string{"not found in package names", "No provider of"}
	zypperURLRe         = regexp.MustCompile(`https?://[^\s'"]+`)
	zypperRepoNameRe    = regexp.MustCompile(`[Rr]epository '([^']+)'`)
	zypperNotFoundRe    = regexp.MustCompile(`(?:Package|No provider of) '([^']+)'`)
)

func classifyZypperFailure(err error, detail string) *ZypperError {
	status := -1
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		status = exit.ExitCode()
	}
	lower := strings.ToLower(detail)
	has := func(texts []string) bool {
		for _, text := range texts {
			if strings.Contains(lower, strings.ToLower(text)) {
				return true
			}
		}
		return false
	}

	switch {
	case status == zypperExitLockedStatus || has(zypperLockedTexts):
		return &ZypperError{Kind: "locked", Message: "Outro programa está instalando ou atualizando pacotes neste momento (por exemplo, a verificação automática de atualizações). Aguarde alguns instantes e tente novamente."}
	case has(zypperSpaceTexts):
		return &ZypperError{Kind: "disk-space", Message: "Não há espaço livre suficiente no disco para concluir a operação. Libere espaço (em Limpeza, por exemplo) e tente novamente."}
	case has(zypperSignTexts):
		what := "um dos pacotes"
		if pkg, _ := zypperPackageURL(detail); pkg != "" {
			what = pkg
		}
		return &ZypperError{Kind: "signature", Message: fmt.Sprintf("Não foi possível confirmar a autenticidade de %s: a assinatura ou a soma de verificação não confere. Por segurança, ele não foi instalado. Tente novamente mais tarde; se o problema continuar, o repositório pode estar com defeito.", what)}
	case has(zypperDownloadTexts) || (status == zypperExitCommitError && has(zypperNetworkTexts)):
		if pkg, host := zypperPackageURL(detail); pkg != "" || !zypperMentionsMetadata(detail) {
			return &ZypperError{Kind: "download", Message: downloadFailureMessage(pkg, host)}
		}
		return &ZypperError{Kind: "repository", Message: repositoryFailureMessage(detail)}
	case has(zypperNetworkTexts) || has(zypperRepoTexts):
		return &ZypperError{Kind: "repository", Message: repositoryFailureMessage(detail)}
	case status == zypperExitNotFound || has(zypperNotFoundTexts):
		what := "O pacote pedido"
		if match := zypperNotFoundRe.FindStringSubmatch(detail); match != nil {
			what = fmt.Sprintf("O pacote %s", match[1])
		}
		return &ZypperError{Kind: "not-found", Message: what + " não foi encontrado nos repositórios ativos. Confira se o repositório de onde ele vem está ativado em Repositórios e tente novamente."}
	case status == zypperExitScriptFailed:
		return &ZypperError{Kind: "script", Message: "Os pacotes foram instalados, mas um script de instalação de um deles falhou. O programa pode não funcionar corretamente; se isso acontecer, remova-o e instale de novo."}
	case status == zypperExitZyppError && strings.Contains(detail, "Problem:"):
		return &ZypperError{Kind: "conflict", Message: "A operação não pôde ser feita porque entraria em conflito com pacotes já instalados ou depende de algo indisponível. Nada foi alterado. Detalhe: " + zypperProblem(detail)}
	}
	return nil
}

func downloadFailureMessage(pkg, host string) string {
	what := "um dos pacotes"
	if pkg != "" {
		what = pkg
	}
	from := "do repositório"
	if host != "" {
		from = "de " + host
	}
	return fmt.Sprintf("Não foi possível baixar %s %s: a conexão com o servidor falhou ou foi interrompida. Verifique a internet e tente novamente em alguns minutos.", what, from)
}

func repositoryFailureMessage(detail string) string {
	which := "um dos repositórios"
	if match := zypperRepoNameRe.FindStringSubmatch(detail); match != nil {
		which = "o repositório " + match[1]
	} else if raw := zypperURLRe.FindString(detail); raw != "" {
		if parsed, err := url.Parse(raw); err == nil && parsed.Hostname() != "" {
			which = "o repositório em " + parsed.Hostname()
		}
	}
	return fmt.Sprintf("Não foi possível acessar %s: o servidor não respondeu ou a conexão falhou. Verifique a internet e tente novamente em alguns minutos.", which)
}

// zypperPackageURL returns the package name and host of the first *.rpm URL
// in detail, or empty strings when there is none.
func zypperPackageURL(detail string) (pkg, host string) {
	for _, raw := range zypperURLRe.FindAllString(detail, -1) {
		parsed, err := url.Parse(strings.TrimRight(raw, ".,:;)"))
		if err != nil || !strings.HasSuffix(parsed.Path, ".rpm") {
			continue
		}
		return rpmFileNameToPackage(path.Base(parsed.Path)), parsed.Hostname()
	}
	return "", ""
}

func zypperMentionsMetadata(detail string) bool {
	return strings.Contains(detail, "/repodata/") || strings.Contains(detail, "repomd.xml") || strings.Contains(detail, "/media.1/")
}

// zypperProblem returns the first "Problem: …" line zypper's solver printed.
func zypperProblem(detail string) string {
	at := strings.Index(detail, "Problem:")
	line := detail[at+len("Problem:"):]
	if end := strings.IndexAny(line, "\n|"); end >= 0 {
		line = line[:end]
	}
	return strings.TrimSpace(line)
}

// rpmFileNameToPackage turns name-version-release.arch.rpm into name, or
// returns "" when the file name does not follow that convention.
func rpmFileNameToPackage(file string) string {
	withoutArch := strings.TrimSuffix(file, path.Ext(strings.TrimSuffix(file, ".rpm"))+".rpm")
	if withoutArch == file {
		return ""
	}
	parts := strings.Split(withoutArch, "-")
	if len(parts) < 3 {
		return ""
	}
	return strings.Join(parts[:len(parts)-2], "-")
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func truncateRunes(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}

// zypperOutputC runs zypper with LC_ALL=C, so explainZypperError can read
// its explanation regardless of the system language.
func zypperOutputC(args ...string) (string, error) {
	cmd := packageCommand("zypper", args...)
	cmd.Env = commandEnvC()
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// explainZypperStreamError explains a runStreamingCmd failure, whose text
// already carries zypper's last output lines.
func explainZypperStreamError(err error) error {
	if err == nil {
		return nil
	}
	return explainZypperError(err, err.Error())
}
