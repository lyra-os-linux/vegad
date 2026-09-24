package distro

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreparationProposalSurvivesProcessAndBindsSource(t *testing.T) {
	t.Setenv("VEGAD_FIRST_UPDATE_MARKER", filepath.Join(t.TempDir(), "first-update.done"))
	original := `<stream><repo-list><repo alias="fixture" name="Fixture" gpgcheck="1"><url>https://user:secret@example.invalid/one</url></repo></repo-list></stream>`
	identity, err := parseRepoKeyIdentity(original, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	key := &UntrustedKeyError{Repo: "fixture", Fingerprint: testApprovedFingerprint, UserId: "Fixture signer"}
	if err := savePreparationKey(key, identity); err != nil {
		t.Fatal(err)
	}
	// Reader has no backend object or pendingKeys map from the producer.
	keys, err := ReadPreparationKey()
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys: %v %v", keys, err)
	}
	record, err := readPreparationKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePreparationApproval(record, keys[0].Repo, keys[0].Fingerprint, keys[0].Token, identity); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(preparationKeyPath())
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "https:") {
		t.Fatal("source credentials leaked into public proposal")
	}
	info, _ := os.Stat(preparationKeyPath())
	if info.Mode().Perm() != 0644 {
		t.Fatal("proposal not readable by query worker")
	}
	for _, changed := range []string{strings.Replace(original, "/one", "/two", 1), strings.Replace(original, "Fixture", "Renamed", 1), strings.Replace(original, `gpgcheck="1"`, `gpgcheck="0"`, 1)} {
		current, err := parseRepoKeyIdentity(changed, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		if validatePreparationApproval(record, "fixture", key.Fingerprint, keys[0].Token, current) == nil {
			t.Fatal("changed source accepted")
		}
	}
	for _, fp := range []string{"01234567", strings.Repeat("A", 40)} {
		if validatePreparationApproval(record, "fixture", fp, keys[0].Token, identity) == nil {
			t.Fatal("changed/short fingerprint accepted")
		}
	}
	if validatePreparationApproval(record, "other", key.Fingerprint, keys[0].Token, identity) == nil {
		t.Fatal("changed alias accepted")
	}
	// Re-proposing the same key for a changed URL must not resurrect old review.
	current, _ := parseRepoKeyIdentity(strings.Replace(original, "/one", "/two", 1), "fixture")
	if err := savePreparationKey(key, current); err != nil {
		t.Fatal(err)
	}
	replacement, _ := readPreparationKey()
	if validatePreparationApproval(replacement, "fixture", key.Fingerprint, keys[0].Token, current) == nil {
		t.Fatal("stale review token accepted")
	}
}
func TestPreparationProposalRejectsCorruption(t *testing.T) {
	t.Setenv("VEGAD_FIRST_UPDATE_MARKER", filepath.Join(t.TempDir(), "first-update.done"))
	if keys, err := ReadPreparationKey(); err != nil || len(keys) != 0 {
		t.Fatalf("missing: %v %v", keys, err)
	}
	if err := os.WriteFile(preparationKeyPath(), []byte(`{"Key":{"Repo":"fixture","Fingerprint":"short"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPreparationKey(); err == nil {
		t.Fatal("corrupt proposal accepted")
	}
}

func TestPreparationAliasesUseEnabledXMLIdentity(t *testing.T) {
	aliases, err := preparationRepoAliases(`<stream><repo-list><repo alias="one" name="Same name" enabled="1"/><repo alias="off" enabled="0"/><repo alias="two" name="Same name" enabled="true"/></repo-list></stream>`)
	if err != nil || len(aliases) != 2 || aliases[0] != "one" || aliases[1] != "two" {
		t.Fatalf("aliases: %v %v", aliases, err)
	}
	if _, err := preparationRepoAliases(`<stream><repo`); err == nil {
		t.Fatal("malformed repo data accepted")
	}
}
