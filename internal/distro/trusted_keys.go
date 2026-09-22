package distro

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// DefaultTrustedKeyringPath is the package-signing keyring shipped by the
// vegad RPM (packaging/keys). It is imported only by `vegad first-update`.
const DefaultTrustedKeyringPath = "/usr/share/vega/keys/lyra-package-signing-keyring.asc"

// trustedPackageSigningFingerprints pins the primary keys of every repository
// the Lyra OS installed system ships with: official Leap 16 (SUSE/openSUSE and
// Backports), home:rodrigosbrito (lyra/vega/fina) and Packman Essentials. The
// keyring file must contain exactly this set; any drift aborts the import.
var trustedPackageSigningFingerprints = []string{
	"1C59D66FCD52563A16933DBCFEC28EAF09D9EA69", // SUSE 16 Package Signing Key
	"F044C2C507A1262B538AAADD8A49EB0325DB7AE0", // openSUSE:Backports OBS Project
	"BF3F9A67D3A2FF98A73F5E07488C583D287A0027", // openSUSE Backports for SUSE Linux 16
	"AD485664E901B867051AB15F35A2F86E29B700A4", // openSUSE Project Signing Key
	"FEAB502539D846DB2C0961CA70AF9E8139DB7C82", // SuSE Package Signing Key
	"7F009157B127B994D5CFBE76F74F09BC3FA1D6CE", // SUSE Package Signing Key
	"399218A6E088C4053F4533BE58097F767EDCA82E", // home:rodrigosbrito OBS Project
	"F8875B880D518B6B8C530D1345A1D0671ABD1AFB", // PackMan Project (signing key)
}

// parseKeyringPrimaryFingerprints reads `gpg --with-colons` output and returns
// the fingerprint of each primary key (the "fpr" record right after "pub").
// Subkey fingerprints are ignored: rpm trusts the primary key it imports.
func parseKeyringPrimaryFingerprints(out string) []string {
	var fingerprints []string
	afterPub := false
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		switch fields[0] {
		case "pub":
			afterPub = true
		case "fpr":
			if afterPub && len(fields) > 9 {
				fingerprints = append(fingerprints, strings.ToUpper(fields[9]))
			}
			afterPub = false
		case "sub", "ssb", "sec":
			afterPub = false
		}
	}
	return fingerprints
}

// checkPinnedFingerprints requires found to be exactly the pinned set, with
// no duplicates, additions or omissions.
func checkPinnedFingerprints(found, pinned []string) error {
	want := make(map[string]bool, len(pinned))
	for _, fpr := range pinned {
		want[fpr] = true
	}
	seen := make(map[string]bool, len(found))
	var unexpected []string
	for _, fpr := range found {
		if !want[fpr] || seen[fpr] {
			unexpected = append(unexpected, fpr)
		}
		seen[fpr] = true
	}
	var missing []string
	for _, fpr := range pinned {
		if !seen[fpr] {
			missing = append(missing, fpr)
		}
	}
	if len(unexpected) > 0 || len(missing) > 0 {
		sort.Strings(unexpected)
		sort.Strings(missing)
		return fmt.Errorf("keyring confiável difere da política (inesperadas: %v; ausentes: %v)", unexpected, missing)
	}
	return nil
}

// keyringFingerprints lists the keyring's primary fingerprints without
// touching any real GnuPG home: --show-keys only parses, and the throwaway
// homedir keeps gpg from creating ~root/.gnupg.
func keyringFingerprints(path string) ([]string, error) {
	home, err := os.MkdirTemp("", "vegad-keyring-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(home)
	cmd := exec.Command("gpg", "--batch", "--no-options", "--homedir", home, "--with-colons", "--show-keys", "--", path)
	cmd.Env = commandEnvC()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ler keyring confiável %s: %w", path, err)
	}
	return parseKeyringPrimaryFingerprints(string(out)), nil
}

// ImportTrustedPackageKeys imports the pinned package-signing keys into the
// RPM database, so Zypper can refresh the shipped repositories without ever
// being told to auto-import whatever key a repository presents. The keyring
// is verified against the pinned fingerprints before rpm sees it. Importing
// a key rpm already has is a no-op.
func ImportTrustedPackageKeys(path string) error {
	found, err := keyringFingerprints(path)
	if err != nil {
		return err
	}
	if err := checkPinnedFingerprints(found, trustedPackageSigningFingerprints); err != nil {
		return err
	}
	if out, err := runCommandOutput("rpmkeys", "--import", "--", path); err != nil {
		return fmt.Errorf("importar chaves confiáveis no RPM: %w — %s", err, out)
	}
	return nil
}
