package distro

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PreparationKey is a reviewable proposal, never an authorization. Token binds
// the displayed fingerprint to the repository configuration observed at discovery.
type PreparationKey struct {
	Repo        string
	Fingerprint string
	UserID      string
	Token       string
}
type preparationKeyRecord struct {
	Key      PreparationKey
	Identity repoKeyIdentity
}

func preparationKeyPath() string {
	if marker := os.Getenv("VEGAD_FIRST_UPDATE_MARKER"); marker != "" {
		return filepath.Join(filepath.Dir(marker), "preparation-key.json")
	}
	return "/var/lib/vega/preparation-key.json"
}
func keyReviewToken(identity repoKeyIdentity, fingerprint string) string {
	data, _ := json.Marshal(struct {
		Identity    repoKeyIdentity
		Fingerprint string
	}{identity, fingerprint})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
func savePreparationKey(key *UntrustedKeyError, identity repoKeyIdentity) error {
	fingerprint, err := normalizeKeyFingerprint(key.Fingerprint)
	if err != nil {
		return err
	}
	record := preparationKeyRecord{PreparationKey{key.Repo, fingerprint, key.UserId, keyReviewToken(identity, fingerprint)}, identity}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	path := preparationKeyPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".preparation-key-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Chmod(0644); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
func readPreparationKey() (preparationKeyRecord, error) {
	var record preparationKeyRecord
	data, err := os.ReadFile(preparationKeyPath())
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	fp, err := normalizeKeyFingerprint(record.Key.Fingerprint)
	if err != nil || record.Key.Repo == "" || record.Key.Repo != record.Identity.Alias || record.Key.Token != keyReviewToken(record.Identity, fp) {
		return record, fmt.Errorf("proposta de chave inválida; repita a preparação")
	}
	return record, nil
}

// ReadPreparationKey does no refresh and can run in the unprivileged query worker.
func ReadPreparationKey() ([]PreparationKey, error) {
	record, err := readPreparationKey()
	if os.IsNotExist(err) {
		return []PreparationKey{}, nil
	}
	if err != nil {
		return nil, err
	}
	return []PreparationKey{record.Key}, nil
}

// DiscoverPreparationKey runs only after the single global refresh rejected a
// key. Probe enabled aliases separately, since display names need not be unique.
// Only the first pending key is exposed; a subsequent retry discovers the next.
func (z *zypperBackend) DiscoverPreparationKey() error {
	z.keyMu.Lock()
	defer z.keyMu.Unlock()
	out, err := z.repoKeyOutput("--xmlout", "--non-interactive", "repos", "--details")
	if err != nil {
		return err
	}
	repos, err := preparationRepoAliases(out)
	if err != nil {
		return err
	}
	var failures []error
	for _, repo := range repos {
		if z.preparationContext != nil && z.preparationContext.Err() != nil {
			return z.preparationContext.Err()
		}
		if err := z.proposePreparationKey(repo); err != nil {
			var key *UntrustedKeyError
			if errors.As(err, &key) {
				return err
			}
			failures = append(failures, err)
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	return fmt.Errorf("a chave pendente mudou; repita a preparação")
}
func (z *zypperBackend) proposePreparationKey(repo string) error {
	err := z.proposeRepoKey(repo)
	var key *UntrustedKeyError
	if errors.As(err, &key) {
		if saveErr := savePreparationKey(key, z.pendingKeys[repo].Identity); saveErr != nil {
			return saveErr
		}
	}
	return err
}
func validatePreparationApproval(record preparationKeyRecord, repo, fingerprint, token string, current repoKeyIdentity) error {
	fp, err := normalizeKeyFingerprint(fingerprint)
	if err != nil {
		return err
	}
	if record.Key.Repo != repo || record.Key.Fingerprint != fp || record.Key.Token != token || record.Identity != current {
		return fmt.Errorf("a proposta ou a identidade do repositório mudou; revise a chave novamente")
	}
	return nil
}
func (z *zypperBackend) TrustPreparationKey(repo, fingerprint, token string, report ProgressFunc) error {
	z.keyMu.Lock()
	defer z.keyMu.Unlock()
	record, err := readPreparationKey()
	if err != nil {
		return fmt.Errorf("carregar proposta de chave: %w", err)
	}
	current, err := readRepoKeyIdentity(repo)
	if err != nil {
		return err
	}
	if err := validatePreparationApproval(record, repo, fingerprint, token, current); err != nil {
		// Refresh the proposal for the originally pending alias, never import on
		// this request. The next dialog must review the new token and fingerprint.
		_ = z.proposePreparationKey(record.Key.Repo)
		return err
	}
	report(0, "Conferindo a chave aprovada...")
	approval := repoKeyApproval{record.Key.Fingerprint, record.Identity}
	if err := refreshWithApprovedKey(repo, approval); err != nil {
		// Changed key prompts are rejected by the existing exact-key responder.
		_ = z.proposePreparationKey(repo)
		return err
	}
	if err := os.Remove(preparationKeyPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	report(100, "Chave aprovada; retomando preparação...")
	return nil
}

func preparationRepoAliases(data string) ([]string, error) {
	decoder := xml.NewDecoder(strings.NewReader(data))
	var aliases []string
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return aliases, nil
		}
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "repo" {
			continue
		}
		var alias, enabled string
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "alias":
				alias = attr.Value
			case "enabled":
				enabled = attr.Value
			}
		}
		if alias != "" && (enabled == "1" || enabled == "true") {
			aliases = append(aliases, alias)
		}
	}
}
