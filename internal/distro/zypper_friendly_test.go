package distro

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func silenceZypperWarnings(t *testing.T) *string {
	t.Helper()
	logged := ""
	previous := zypperWarnf
	zypperWarnf = func(format string, args ...any) { logged = fmt.Sprintf(format, args...) }
	t.Cleanup(func() { zypperWarnf = previous })
	return &logged
}

// The dotnet-sdk-10.0 download from packages.microsoft.com cut off with curl
// error 56 must reach Vega as a plain explanation naming the package and the
// server, not as "exit status 8" followed by libzypp's text, which goes to
// the log; the exit status stays reachable.
func TestZypperTransactionDownloadFailureIsFriendly(t *testing.T) {
	stream := `<?xml version="1.0"?>
<stream>
<message type="error">Download (curl) error for 'https://packages.microsoft.com/opensuse/16/prod/Packages/d/dotnet-sdk-10.0-10.0.403-1.x86_64.rpm':
Error code: Curl error 56
Error message: Recv failure: Connection reset by peer</message>
<message type="error">Problem occurred during or after installation or removal of packages:
Installation has been aborted as directed.</message>
</stream>`
	var messages zypperXMLMessages
	if err := parseZypperXMLStreamMessages(strings.NewReader(stream), nil, nil, &messages); err != nil {
		t.Fatal(err)
	}
	logged := silenceZypperWarnings(t)

	err := zypperTransactionError(exitWith(t, 8), "", messages)
	var friendly *ZypperError
	if !errors.As(err, &friendly) || friendly.Kind != "download" {
		t.Fatalf("error = %#v, want a download ZypperError", err)
	}
	want := "Não foi possível baixar dotnet-sdk-10.0 de packages.microsoft.com: a conexão com o servidor falhou ou foi interrompida. Verifique a internet e tente novamente em alguns minutos."
	if err.Error() != want {
		t.Fatalf("message = %q", err.Error())
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 8 {
		t.Fatalf("exit status must stay reachable: %v", err)
	}
	if !strings.Contains(*logged, "Curl error 56") {
		t.Fatalf("zypper's explanation was not logged: %q", *logged)
	}
}

func TestExplainZypperErrorKinds(t *testing.T) {
	silenceZypperWarnings(t)
	cases := []struct {
		name   string
		status int
		detail string
		kind   string
		want   string
	}{
		{"locked by status", 7, "", "locked", "Outro programa está instalando"},
		{"locked by text", 1, "System management is locked by the application with pid 4242 (zypper).", "locked", "Aguarde alguns instantes"},
		{"package without URL", 8, "Failed to provide Package dotnet-sdk-10.0-10.0.403-1.x86_64. Do you want to retry retrieval?", "download", "Não foi possível baixar um dos pacotes do repositório:"},
		{"package host", 8, "Timeout exceeded when accessing 'https://cdn.opensuse.org/distribution/leap/16.1/repo/oss/x86_64/gh-2.100.0-bp161.1.3.x86_64.rpm'.", "download", "baixar gh de cdn.opensuse.org"},
		{"repository metadata", 4, "Download (curl) error for 'https://packages.microsoft.com/opensuse/16/prod/repodata/repomd.xml':\nError code: Connection failed\nError message: Could not resolve host: packages.microsoft.com", "repository", "acessar o repositório em packages.microsoft.com"},
		{"repository by name", 4, "Repository 'microsoft-prod' is invalid.\nValid metadata not found at specified URL", "repository", "acessar o repositório microsoft-prod"},
		{"disk space", 8, "Installation of dotnet-sdk-10.0 failed: No space left on device", "disk-space", "espaço livre suficiente"},
		{"signature", 8, "Signature verification failed for file 'dotnet-sdk-10.0-10.0.403-1.x86_64.rpm' from repository 'microsoft-prod'.", "signature", "autenticidade de um dos pacotes"},
		{"signature with URL", 8, "File 'https://packages.microsoft.com/opensuse/16/prod/Packages/d/dotnet-host-10.0.14-1.x86_64.rpm' is not signed.", "signature", "autenticidade de dotnet-host"},
		{"not found", 104, "Package 'dotnet-sdk-11.0' not found.", "not-found", "O pacote dotnet-sdk-11.0 não foi encontrado"},
		{"not found without name", 104, "", "not-found", "O pacote pedido não foi encontrado"},
		{"script", 107, "warning: %post(dotnet-sdk-10.0) scriptlet failed, exit status 1", "script", "um script de instalação"},
		{"conflict", 4, "Problem: the installed rust1.98 conflicts with 'rust+rustc' provided by rustup\n Solution 1: deinstallation of rust1.98", "conflict", "Detalhe: the installed rust1.98 conflicts with 'rust+rustc' provided by rustup"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := explainZypperError(exitWith(t, tc.status), tc.detail)
			var friendly *ZypperError
			if !errors.As(err, &friendly) {
				t.Fatalf("error = %v, want a ZypperError", err)
			}
			if friendly.Kind != tc.kind || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("kind = %q, message = %q; want %q containing %q", friendly.Kind, err.Error(), tc.kind, tc.want)
			}
		})
	}
}

// What the layer cannot explain keeps the original error and its detail.
func TestExplainZypperErrorLeavesUnknownFailures(t *testing.T) {
	silenceZypperWarnings(t)
	original := fmt.Errorf("zypper addrepo x: %w — %s", exitWith(t, 4), "Repository named 'x' already exists.")
	if err := explainZypperError(original, "Repository named 'x' already exists."); err != original {
		t.Fatalf("unknown failure was rewritten: %v", err)
	}
	if err := explainZypperError(nil, "Curl error 56"); err != nil {
		t.Fatalf("nil error became %v", err)
	}
	// An explained error is not explained twice.
	once := explainZypperError(exitWith(t, 7), "")
	if twice := explainZypperError(fmt.Errorf("wrap: %w", once), ""); !strings.HasPrefix(twice.Error(), "wrap:") {
		t.Fatalf("explained error was rewritten again: %v", twice)
	}
}

// Streaming commands (kernel install, cache cleanup) carry zypper's last
// lines in the error text; that is enough to explain them.
func TestExplainZypperStreamError(t *testing.T) {
	silenceZypperWarnings(t)
	err := fmt.Errorf("%s: %w — %s", "/bin/sh", exitWith(t, 7), "System management is locked by the application with pid 4242 (zypper).")
	var friendly *ZypperError
	if !errors.As(explainZypperStreamError(err), &friendly) || friendly.Kind != "locked" {
		t.Fatalf("stream error was not explained: %v", err)
	}
	if explainZypperStreamError(nil) != nil {
		t.Fatal("nil stream error must stay nil")
	}
}

func TestRPMFileNameToPackage(t *testing.T) {
	for file, want := range map[string]string{
		"dotnet-sdk-10.0-10.0.403-1.x86_64.rpm":           "dotnet-sdk-10.0",
		"gh-fish-completion-2.100.0-bp161.1.3.noarch.rpm": "gh-fish-completion",
		"aspnetcore-runtime-10.0-10.0.14-1.x86_64.rpm":    "aspnetcore-runtime-10.0",
		"broken.rpm":    "",
		"not-a-package": "",
	} {
		if got := rpmFileNameToPackage(file); got != want {
			t.Errorf("rpmFileNameToPackage(%q) = %q, want %q", file, got, want)
		}
	}
}
