package dbusserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/parental"
)

type parentalBootstrapTestPermission struct{}

func (parentalBootstrapTestPermission) CurrentEnrollment() (string, string, bool, error) {
	return strings.Repeat("1", 32), strings.Repeat("2", 32), false, nil
}

type parentalBootstrapTestProvisioner struct{ issuer string }

func (p parentalBootstrapTestProvisioner) Issuer() string { return p.issuer }
func (parentalBootstrapTestProvisioner) Initialize(parental.RecoveryReference, string, func() error) (parental.RecoveryReference, error) {
	return parental.RecoveryReference{}, nil
}
func (parentalBootstrapTestProvisioner) ResumeInitial(parental.RecoveryReference, string, func() error) (parental.RecoveryReference, error) {
	return parental.RecoveryReference{}, nil
}

func parentalBootstrapTestKeyring(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg is required for the isolated public-key fixture")
	}
	home := filepath.Join(t.TempDir(), "gnupg")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	args := []string{"--batch", "--no-tty", "--homedir", home, "--passphrase", "", "--quick-generate-key", "Lyra bootstrap factory fixture", "ed25519", "sign", "0"}
	if output, err := exec.Command("gpg", args...).CombinedOutput(); err != nil {
		t.Fatalf("generate ephemeral public-key fixture: %v: %s", err, output)
	}
	listing, err := exec.Command("gpg", "--batch", "--no-tty", "--homedir", home, "--with-colons", "--list-keys").Output()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := ""
	for _, line := range strings.Split(string(listing), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) > 9 && fields[0] == "fpr" {
			fingerprint = strings.ToUpper(fields[9])
			break
		}
	}
	if fingerprint == "" {
		t.Fatal("ephemeral primary fingerprint missing")
	}
	keyring := filepath.Join(t.TempDir(), "parental.gpg")
	key, err := exec.Command("gpg", "--batch", "--no-tty", "--homedir", home, "--export", fingerprint).Output()
	if err != nil || len(key) == 0 {
		t.Fatalf("export ephemeral public key: %v", err)
	}
	if err := os.WriteFile(keyring, key, 0o600); err != nil {
		t.Fatal(err)
	}
	return keyring, fingerprint
}

func TestParentalIssuerConfigRequiresClosedHTTPSContract(t *testing.T) {
	valid := "schema=1\nendpoint=https://issuer.example/v1/first-reference\nissuer=" + strings.Repeat("a", 32) + "\nfingerprint=" + strings.Repeat("A", 40) + "\n"
	cfg, err := parseParentalIssuerConfig([]byte(valid))
	if err != nil || cfg.Endpoint != "https://issuer.example/v1/first-reference" || cfg.Issuer != strings.Repeat("a", 32) || cfg.Fingerprint != strings.Repeat("A", 40) {
		t.Fatalf("valid config = %#v, %v", cfg, err)
	}
	for name, data := range map[string][]byte{
		"empty":               nil,
		"oversized":           []byte(strings.Repeat("x", parentalIssuerConfigLimit+1)),
		"missing field":       []byte("schema=1\nissuer=" + strings.Repeat("a", 32) + "\nfingerprint=" + strings.Repeat("A", 40) + "\n"),
		"duplicate field":     []byte(valid + "issuer=" + strings.Repeat("a", 32) + "\n"),
		"unknown private key": []byte(valid + "private_key=do-not-allow\n"),
		"unsupported schema":  []byte(strings.Replace(valid, "schema=1", "schema=2", 1)),
		"plaintext endpoint":  []byte(strings.Replace(valid, "https://", "http://", 1)),
		"wrong path":          []byte(strings.Replace(valid, "/v1/first-reference", "/", 1)),
		"query endpoint":      []byte(strings.Replace(valid, "/v1/first-reference", "/v1/first-reference?token=x", 1)),
		"fragment endpoint":   []byte(strings.Replace(valid, "/v1/first-reference", "/v1/first-reference#x", 1)),
		"userinfo endpoint":   []byte(strings.Replace(valid, "issuer.example", "user:pass@issuer.example", 1)),
		"invalid port":        []byte(strings.Replace(valid, "issuer.example", "issuer.example:99999", 1)),
		"empty port":          []byte(strings.Replace(valid, "issuer.example", "issuer.example:", 1)),
		"escaped route":       []byte(strings.Replace(valid, "/v1/first-reference", "/v1%2Ffirst-reference", 1)),
		"invalid issuer":      []byte(strings.Replace(valid, strings.Repeat("a", 32), "not-an-id", 1)),
		"invalid fingerprint": []byte(strings.Replace(valid, strings.Repeat("A", 40), strings.Repeat("g", 40), 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseParentalIssuerConfig(data); err == nil {
				t.Fatal("invalid issuer config was accepted")
			}
		})
	}
}

func TestReadParentalIssuerConfigRejectsMissingAndSymlinkedFiles(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.conf")
	if _, err := readParentalIssuerConfig(missing); err != parental.RecoveryRequired {
		t.Fatalf("missing config error = %v", err)
	}
	target := filepath.Join(t.TempDir(), "issuer.conf")
	if err := os.WriteFile(target, []byte("schema=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link.conf")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readParentalIssuerConfig(link); err != parental.RecoveryRequired {
		t.Fatalf("symlinked config error = %v", err)
	}
}

func TestInstalledParentalBootstrapRemainsUnavailableWithoutTrustConfig(t *testing.T) {
	if _, err := os.Stat(parentalIssuerConfigPath); err == nil {
		t.Skip("host has parental issuer configuration; do not exercise host bootstrap")
	}
	bootstrap, err := newInstalledParentalEnrollmentBootstrap(dbus.Sender(":1.55"))
	if bootstrap != nil || err != parental.RecoveryRequired {
		t.Fatalf("unconfigured bootstrap = %#v, %v", bootstrap, err)
	}
}

func TestParentalBootstrapFactoryRequiresEveryTrustGate(t *testing.T) {
	keyring, fingerprint := parentalBootstrapTestKeyring(t)
	issuer := strings.Repeat("a", 32)
	config := parentalIssuerConfig{Endpoint: "https://issuer.example/v1/first-reference", Issuer: issuer, Fingerprint: fingerprint}

	t.Run("valid trust reaches topology gate", func(t *testing.T) {
		calls := 0
		bootstrap, err := buildParentalEnrollmentBootstrap(config, keyring, dbus.Sender(":1.55"), func(gotIssuer string) (parentalEnrollmentDependencies, error) {
			calls++
			if gotIssuer != issuer {
				t.Fatalf("dependency factory issuer = %q", gotIssuer)
			}
			return parentalEnrollmentDependencies{permission: parentalBootstrapTestPermission{}, provisioner: parentalBootstrapTestProvisioner{issuer: issuer}}, nil
		})
		if err != nil || bootstrap == nil || calls != 1 {
			t.Fatalf("valid factory = %#v, %v, dependency calls=%d", bootstrap, err, calls)
		}
	})

	t.Run("fingerprint mismatch blocks topology", func(t *testing.T) {
		bad := config
		bad.Fingerprint = strings.Repeat("F", len(fingerprint))
		calls := 0
		bootstrap, err := buildParentalEnrollmentBootstrap(bad, keyring, dbus.Sender(":1.55"), func(string) (parentalEnrollmentDependencies, error) {
			calls++
			return parentalEnrollmentDependencies{permission: parentalBootstrapTestPermission{}, provisioner: parentalBootstrapTestProvisioner{issuer: issuer}}, nil
		})
		if bootstrap != nil || err != parental.RecoveryRequired || calls != 0 {
			t.Fatalf("mismatched pin reached topology: %#v, %v, calls=%d", bootstrap, err, calls)
		}
	})

	t.Run("malformed endpoint blocks before keyring", func(t *testing.T) {
		bad := config
		bad.Endpoint = "https://issuer.example:99999/v1/first-reference"
		calls := 0
		bootstrap, err := buildParentalEnrollmentBootstrap(bad, filepath.Join(t.TempDir(), "missing.gpg"), dbus.Sender(":1.55"), func(string) (parentalEnrollmentDependencies, error) {
			calls++
			return parentalEnrollmentDependencies{}, nil
		})
		if bootstrap != nil || err != parental.RecoveryRequired || calls != 0 {
			t.Fatalf("invalid endpoint passed preflight: %#v, %v, calls=%d", bootstrap, err, calls)
		}
	})

	t.Run("topology closed", func(t *testing.T) {
		bootstrap, err := buildParentalEnrollmentBootstrap(config, keyring, dbus.Sender(":1.55"), func(string) (parentalEnrollmentDependencies, error) {
			return parentalEnrollmentDependencies{}, parental.RecoveryRequired
		})
		if bootstrap != nil || err != parental.RecoveryRequired {
			t.Fatalf("closed topology created bootstrap: %#v, %v", bootstrap, err)
		}
	})

	for _, tc := range []struct {
		name string
		deps parentalEnrollmentDependencies
	}{
		{name: "missing permission source", deps: parentalEnrollmentDependencies{provisioner: parentalBootstrapTestProvisioner{issuer: issuer}}},
		{name: "missing provisioner", deps: parentalEnrollmentDependencies{permission: parentalBootstrapTestPermission{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bootstrap, err := buildParentalEnrollmentBootstrap(config, keyring, dbus.Sender(":1.55"), func(string) (parentalEnrollmentDependencies, error) {
				return tc.deps, nil
			})
			if bootstrap != nil || err != parental.RecoveryRequired {
				t.Fatalf("incomplete dependencies created bootstrap: %#v, %v", bootstrap, err)
			}
		})
	}

	t.Run("issuer mismatch in provisioner", func(t *testing.T) {
		bootstrap, err := buildParentalEnrollmentBootstrap(config, keyring, dbus.Sender(":1.55"), func(string) (parentalEnrollmentDependencies, error) {
			return parentalEnrollmentDependencies{permission: parentalBootstrapTestPermission{}, provisioner: parentalBootstrapTestProvisioner{issuer: strings.Repeat("b", 32)}}, nil
		})
		if bootstrap != nil || err != parental.RecoveryRequired {
			t.Fatalf("issuer mismatch created bootstrap: %#v, %v", bootstrap, err)
		}
	})

	t.Run("missing sender", func(t *testing.T) {
		calls := 0
		bootstrap, err := buildParentalEnrollmentBootstrap(config, keyring, "", func(string) (parentalEnrollmentDependencies, error) {
			calls++
			return parentalEnrollmentDependencies{}, nil
		})
		if bootstrap != nil || err != parental.Denied || calls != 0 {
			t.Fatalf("missing sender passed factory: %#v, %v, calls=%d", bootstrap, err, calls)
		}
	})
}

func TestParentalEnrollmentReadinessReportsOnlyLocalTrustAndTopology(t *testing.T) {
	keyring, fingerprint := parentalBootstrapTestKeyring(t)
	issuer := strings.Repeat("a", 32)
	config := parentalIssuerConfig{Endpoint: "https://127.0.0.1:1/v1/first-reference", Issuer: issuer, Fingerprint: fingerprint}
	dependencies := func(gotIssuer string) (parentalEnrollmentDependencies, error) {
		if gotIssuer != issuer {
			t.Fatalf("dependency factory issuer = %q", gotIssuer)
		}
		return parentalEnrollmentDependencies{permission: parentalBootstrapTestPermission{}, provisioner: parentalBootstrapTestProvisioner{issuer: issuer}}, nil
	}
	if got := assessParentalEnrollmentReadiness(config, keyring, dependencies); got != parentalEnrollmentReady {
		t.Fatalf("valid local prerequisites readiness = %v", got)
	}

	calls := 0
	closedTopology := func(string) (parentalEnrollmentDependencies, error) {
		calls++
		return parentalEnrollmentDependencies{}, parental.RecoveryRequired
	}
	if got := assessParentalEnrollmentReadiness(config, keyring, closedTopology); got != parentalEnrollmentUnavailable || calls != 1 {
		t.Fatalf("closed topology readiness = %v, dependency calls=%d", got, calls)
	}

	calls = 0
	wrongPin := config
	wrongPin.Fingerprint = strings.Repeat("F", len(fingerprint))
	if got := assessParentalEnrollmentReadiness(wrongPin, keyring, func(string) (parentalEnrollmentDependencies, error) {
		calls++
		return parentalEnrollmentDependencies{}, nil
	}); got != parentalEnrollmentUnavailable || calls != 0 {
		t.Fatalf("wrong pin readiness = %v, dependency calls=%d", got, calls)
	}

	badEndpoint := config
	badEndpoint.Endpoint = "http://127.0.0.1/v1/first-reference"
	if got := assessParentalEnrollmentReadiness(badEndpoint, keyring, dependencies); got != parentalEnrollmentUnavailable {
		t.Fatalf("invalid endpoint readiness = %v", got)
	}
	if got := assessParentalEnrollmentReadiness(config, filepath.Join(t.TempDir(), "missing.gpg"), dependencies); got != parentalEnrollmentUnavailable {
		t.Fatalf("missing keyring readiness = %v", got)
	}
}
