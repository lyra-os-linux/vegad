package distro

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestParseKeyringPrimaryFingerprintsSkipsSubkeys(t *testing.T) {
	out := "pub:-:4096:1:AAAA:1:::-:::scESC::::::23::0:\n" +
		"fpr:::::::::1111111111111111111111111111111111111111:\n" +
		"uid:-::::1::HASH::Example <a@example.org>::::::::::0:\n" +
		"sub:-:4096:1:BBBB:1::::::e::::::23:\n" +
		"fpr:::::::::2222222222222222222222222222222222222222:\n" +
		"pub:-:2048:1:CCCC:1:::-:::scESC::::::23::0:\n" +
		"fpr:::::::::3333333333333333333333333333333333333333:\n"
	got := parseKeyringPrimaryFingerprints(out)
	want := []string{
		"1111111111111111111111111111111111111111",
		"3333333333333333333333333333333333333333",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fingerprints = %v, want %v", got, want)
	}
}

func TestCheckPinnedFingerprints(t *testing.T) {
	pinned := []string{"A", "B"}
	for _, tc := range []struct {
		name  string
		found []string
		ok    bool
	}{
		{"exact", []string{"B", "A"}, true},
		{"missing", []string{"A"}, false},
		{"extra", []string{"A", "B", "C"}, false},
		{"duplicate", []string{"A", "B", "A"}, false},
		{"empty", nil, false},
	} {
		err := checkPinnedFingerprints(tc.found, pinned)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

// The shipped keyring and the pinned list are edited together; this keeps a
// key swap in one from silently diverging from the other.
func TestShippedKeyringMatchesPinnedFingerprints(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg not installed")
	}
	found, err := keyringFingerprints("../../packaging/keys/lyra-package-signing-keyring.asc")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkPinnedFingerprints(found, trustedPackageSigningFingerprints); err != nil {
		t.Fatal(err)
	}
}

// Exercise actual RPM idempotence in a private database. The PATH wrapper only
// redirects rpmkeys to this database; no host trust settings are modified.
func TestTrustedPackageKeysPrivateRPMDatabase(t *testing.T) {
	rpm, err := exec.LookPath("rpm")
	if err != nil {
		t.Skip("rpm not installed")
	}
	rpmkeys, err := exec.LookPath("rpmkeys")
	if err != nil {
		t.Skip("rpmkeys not installed")
	}
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg not installed")
	}
	root := t.TempDir()
	db := filepath.Join(root, "rpmdb")
	if out, err := exec.Command(rpm, "--dbpath", db, "--initdb").CombinedOutput(); err != nil {
		t.Fatalf("init private RPM database: %v: %s", err, out)
	}
	wrapper := filepath.Join(root, "rpmkeys")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf 'import\\n' >> \"$VEGAD_TEST_IMPORT_LOG\"\nexec \"$VEGAD_TEST_RPMKEYS\" --dbpath \"$VEGAD_TEST_RPMDB\" \"$@\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VEGAD_TEST_RPMKEYS", rpmkeys)
	t.Setenv("VEGAD_TEST_RPMDB", db)
	imports := filepath.Join(root, "imports")
	t.Setenv("VEGAD_TEST_IMPORT_LOG", imports)
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	inventory := func() []string {
		t.Helper()
		out, err := exec.Command(rpm, "--dbpath", db, "-qa").CombinedOutput()
		if err != nil {
			t.Fatalf("private RPM inventory: %v: %s", err, out)
		}
		keys := strings.Fields(string(out))
		sort.Strings(keys)
		return keys
	}
	path := "../../packaging/keys/lyra-package-signing-keyring.asc"
	if err := ImportTrustedPackageKeys(path); err != nil {
		t.Fatal(err)
	}
	first := inventory()
	if len(first) != len(trustedPackageSigningFingerprints) {
		t.Fatalf("imported %d keys, want %d", len(first), len(trustedPackageSigningFingerprints))
	}
	if err := ImportTrustedPackageKeys(path); err != nil {
		t.Fatal(err)
	}
	if got := inventory(); !reflect.DeepEqual(got, first) {
		t.Fatalf("reimport changed RPM inventory: %v -> %v", first, got)
	}
	// A partial shipped keyring must not mutate the already trusted database.
	gnupg := filepath.Join(root, "gnupg")
	if err := os.Mkdir(gnupg, 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("gpg", "--batch", "--no-options", "--homedir", gnupg, "--import", path).CombinedOutput(); err != nil {
		t.Fatalf("private GPG import: %v: %s", err, out)
	}
	data, err := exec.Command("gpg", "--batch", "--no-options", "--homedir", gnupg, "--armor", "--export", trustedPackageSigningFingerprints[0]).Output()
	if err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(root, "partial.asc")
	if err := os.WriteFile(partial, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := ImportTrustedPackageKeys(partial); err == nil {
		t.Fatal("partial keyring accepted")
	}
	if got := inventory(); !reflect.DeepEqual(got, first) {
		t.Fatal("rejected keyring changed RPM database")
	}
	if data, err := os.ReadFile(imports); err != nil || string(data) != "import\nimport\n" {
		t.Fatalf("rejected keyring reached RPM: %q, %v", data, err)
	}
}
