package dbusserver

import (
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/parental"
	"golang.org/x/sys/unix"
)

const (
	parentalIssuerConfigPath  = "/etc/vega/parental-issuer.conf"
	parentalIssuerKeyringPath = "/usr/share/vega/keys/lyra-parental-issuer.gpg"
	parentalIssuerConfigLimit = 4096
)

var parentalIssuerIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var parentalIssuerFingerprintPattern = regexp.MustCompile(`^(?:[A-F0-9]{40}|[A-F0-9]{64})$`)

type parentalIssuerConfig struct {
	Endpoint, Issuer, Fingerprint string
}

func (c parentalIssuerConfig) validate() error {
	if !parentalIssuerIDPattern.MatchString(c.Issuer) || !parentalIssuerFingerprintPattern.MatchString(c.Fingerprint) {
		return parental.RecoveryRequired
	}
	endpoint, err := url.Parse(c.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.Path != "/v1/first-reference" || endpoint.RawPath != "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.RawFragment != "" || endpoint.Opaque != "" || strings.ContainsAny(c.Endpoint, "?#") || strings.HasSuffix(endpoint.Host, ":") {
		return parental.RecoveryRequired
	}
	if port := endpoint.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return parental.RecoveryRequired
		}
	}
	return nil
}

// parseParentalIssuerConfig accepts a tiny closed key=value schema. The file
// contains public trust configuration only; signing secrets never belong on
// this client. No default endpoint or public-key fingerprint is fabricated.
func parseParentalIssuerConfig(data []byte) (parentalIssuerConfig, error) {
	if len(data) == 0 || len(data) > parentalIssuerConfigLimit || !utf8.Valid(data) {
		return parentalIssuerConfig{}, parental.RecoveryRequired
	}
	out := parentalIssuerConfig{}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || value == "" || seen[key] {
			return parentalIssuerConfig{}, parental.RecoveryRequired
		}
		seen[key] = true
		switch key {
		case "schema":
			if value != "1" {
				return parentalIssuerConfig{}, parental.RecoveryRequired
			}
		case "endpoint":
			out.Endpoint = value
		case "issuer":
			out.Issuer = value
		case "fingerprint":
			out.Fingerprint = value
		default:
			return parentalIssuerConfig{}, parental.RecoveryRequired
		}
	}
	if !seen["schema"] || !seen["endpoint"] || !seen["issuer"] || !seen["fingerprint"] || out.validate() != nil {
		return parentalIssuerConfig{}, parental.RecoveryRequired
	}
	return out, nil
}

func readParentalIssuerConfig(path string) (parentalIssuerConfig, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return parentalIssuerConfig{}, parental.RecoveryRequired
	}
	file := os.NewFile(uintptr(fd), "parental-issuer-config")
	defer file.Close()
	var before unix.Stat_t
	if unix.Fstat(fd, &before) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Uid != 0 || before.Mode&0022 != 0 || before.Nlink != 1 || before.Size <= 0 || before.Size > parentalIssuerConfigLimit {
		return parentalIssuerConfig{}, parental.RecoveryRequired
	}
	data, err := io.ReadAll(io.LimitReader(file, parentalIssuerConfigLimit+1))
	if err != nil || len(data) == 0 || len(data) > parentalIssuerConfigLimit {
		return parentalIssuerConfig{}, parental.RecoveryRequired
	}
	var after unix.Stat_t
	if unix.Fstat(fd, &after) != nil || before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim {
		return parentalIssuerConfig{}, parental.RecoveryRequired
	}
	return parseParentalIssuerConfig(data)
}

type parentalEnrollmentDependencies struct {
	permission  parental.EnrollmentOperationSource
	provisioner parental.InitialReferenceProvisioner
}

type parentalEnrollmentDependencyFactory func(string) (parentalEnrollmentDependencies, error)

type parentalEnrollmentReadiness uint8

const (
	parentalEnrollmentUnavailable parentalEnrollmentReadiness = iota
	parentalEnrollmentReady
)

// assessParentalEnrollmentReadiness reports only whether all trusted local
// prerequisites exist. It constructs, but never uses, the HTTPS client, so it
// does not contact the issuer or expose configured identifiers to callers.
func assessParentalEnrollmentReadiness(config parentalIssuerConfig, keyringPath string, dependencies parentalEnrollmentDependencyFactory) parentalEnrollmentReadiness {
	if config.validate() != nil || dependencies == nil {
		return parentalEnrollmentUnavailable
	}
	verifier := parental.NewSignedFirstReferenceVerifier(config.Issuer, config.Fingerprint, keyringPath)
	if verifier.ValidateKeyring() != nil {
		return parentalEnrollmentUnavailable
	}
	if _, err := parental.NewSignedFirstReferenceClient(config.Endpoint, verifier); err != nil {
		return parentalEnrollmentUnavailable
	}
	deps, err := dependencies(config.Issuer)
	if err != nil || deps.permission == nil || deps.provisioner == nil || deps.provisioner.Issuer() != config.Issuer {
		return parentalEnrollmentUnavailable
	}
	return parentalEnrollmentReady
}

func installedParentalEnrollmentReadiness() parentalEnrollmentReadiness {
	config, err := readParentalIssuerConfig(parentalIssuerConfigPath)
	if err != nil {
		return parentalEnrollmentUnavailable
	}
	return assessParentalEnrollmentReadiness(config, parentalIssuerKeyringPath, func(issuer string) (parentalEnrollmentDependencies, error) {
		provisioner, permission := parental.NewESPBackedReferenceProvisioner(issuer)
		if provisioner == nil || permission == nil {
			return parentalEnrollmentDependencies{}, parental.RecoveryRequired
		}
		return parentalEnrollmentDependencies{permission: permission, provisioner: provisioner}, nil
	})
}

func buildParentalEnrollmentBootstrap(config parentalIssuerConfig, keyringPath string, sender dbus.Sender, dependencies parentalEnrollmentDependencyFactory) (*parental.FirstEnrollmentBootstrap, error) {
	if sender == "" {
		return nil, parental.Denied
	}
	if config.validate() != nil || dependencies == nil {
		return nil, parental.RecoveryRequired
	}
	verifier := parental.NewSignedFirstReferenceVerifier(config.Issuer, config.Fingerprint, keyringPath)
	if err := verifier.ValidateKeyring(); err != nil {
		return nil, parental.RecoveryRequired
	}
	fetcher, err := parental.NewSignedFirstReferenceClient(config.Endpoint, verifier)
	if err != nil {
		return nil, parental.RecoveryRequired
	}
	deps, err := dependencies(config.Issuer)
	if err != nil || deps.permission == nil || deps.provisioner == nil || deps.provisioner.Issuer() != config.Issuer {
		return nil, parental.RecoveryRequired
	}
	authorize, revalidate := parentalEnrollmentAuthorization(sender)
	return parental.NewFirstEnrollmentBootstrap(config.Issuer, config.Fingerprint, deps.permission, fetcher, deps.provisioner, authorize, revalidate), nil
}

// newInstalledParentalEnrollmentBootstrap constructs the internal coordinator
// only when service-owned issuer trust and the independently qualified ESP /
// Btrfs layout are available. It is deliberately not exported through D-Bus.
func newInstalledParentalEnrollmentBootstrap(sender dbus.Sender) (*parental.FirstEnrollmentBootstrap, error) {
	config, err := readParentalIssuerConfig(parentalIssuerConfigPath)
	if err != nil {
		return nil, err
	}
	return buildParentalEnrollmentBootstrap(config, parentalIssuerKeyringPath, sender, func(issuer string) (parentalEnrollmentDependencies, error) {
		provisioner, permission := parental.NewESPBackedReferenceProvisioner(issuer)
		if provisioner == nil || permission == nil {
			return parentalEnrollmentDependencies{}, parental.RecoveryRequired
		}
		return parentalEnrollmentDependencies{permission: permission, provisioner: provisioner}, nil
	})
}
