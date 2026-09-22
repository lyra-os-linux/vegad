package distro

import (
	"os/exec"
	"reflect"
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
