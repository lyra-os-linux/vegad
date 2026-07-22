package distro

import "testing"

// testFactoryKey is openSUSE:Factory's real repomd.xml.key (an unrelated,
// well-known public repo, fetched while building this feature just to
// validate `gpg --with-colons --show-keys`'s field layout against a real
// key rather than guessing) — embedded here so this test needs no network.
const testFactoryKey = `-----BEGIN PGP PUBLIC KEY BLOCK-----

mQINBGKwfiIBEADe9bKROWax5CI83KUly/ZRDtiCbiSnvWfBK1deAttV+qLTZ006
090eQCOlMtcjhNe641Ahi/SwMsBLNMNich7/ddgNDJ99H8Oen6mBze00Z0Nlg2HZ
VZibSFRYvg+tdivu83a1A1Z5U10Fovwc2awCVWs3i6/XrpXiKZP5/Pi3RV2K7VcG
rt+TUQ3ygiCh1FhKnBfIGS+UMhHwdLUAQ5cB+7eAgba5kSvlWKRymLzgAPVkB/NJ
uqjz+yPZ9LtJZXHYrjq9yaEy0J80Mn9uTmVggZqdTPWx5CnIWv7Y3fnWbkL/uhTR
uDmNfy7a0ULB3qjJXMAnjLE/Oi14UE28XfMtlEmEEeYhtlPlH7hvFDgirRHN6kss
BvOpT+UikqFhJ+IsarAqnnrEbD2nO7Jnt6wnYf9QWPnl93h2e0/qi4JqT9zw93zs
fDENY/yhTuqqvgN6dqaD2ABBNeQENII+VpqjzmnEl8TePPCOb+pELQ7uk6j4D0j7
slQjdns/wUHg8bGE3uMFcZFkokPv6Cw6Aby1ijqBe+qYB9ay7nki44OoOsJvirxv
p00MRgsm+C8he+B8QDZNBWYiPkhHZBFi5GQSUY04FimR2BpudV9rJqbKP0UezEpc
m3tmqLuIc9YCxqMt40tbQOUVSrtFcYlltJ/yTVxu3plUpwtJGQavCJM7RQARAQAB
tDRvcGVuU1VTRSBQcm9qZWN0IFNpZ25pbmcgS2V5IDxvcGVuc3VzZUBvcGVuc3Vz
ZS5vcmc+iQJVBBMBCAA/AhsDBgsJCAcDAgYVCAIJCgsEFgIDAQIeAQIXgBYhBK1I
VmTpAbhnBRqxXzWi+G4ptwCkBQJqF/o4BQkO7EoWAAoJEDWi+G4ptwCkD4EQAL7q
mTQK6Am7lfb7e/WCjuyQEEh43y/S4WM4qwNgvpveUzuGcS+q78kLj3FzqEEH2MuL
4cBN2tdNogXKzMKGUOqjw4vycinVwGRiR+a7guhUYS/syusLIgT+jTk9p4qMxW4D
83iceavzisNO7Vp7kI0pbiTrrgc6IeVVMf1pJiGyQaXNGYGAUPMaEs5bgLFnQORR
zP57IM6BkiL6zHv2wIV1RZPzzseWCOYPxRVYlAn8s0oZULWHAfTcOMWodf3mpgAa
RuWX/UXbMgugtfpeWJpzrIiuO3EkDfbqEtPZBBQH27SZheSojqYdIeL2uJ/SqHZe
enJ8YaO7KpKShGQuSpId8T5GTHx0BcFoctHGeZ8wSBFLO2i6Cf2fjsI3J8qY5k8V
a2b1//Vz62GTbmppRrS3LBtEiMKOCeypQqiiFQzGEUvEPFgC9wzet1jVjzaW5Pa3
mULzmfPPXcK0OtwYEZAvnwkI+ZurYEVA3zJo7sqDKlKfw/LszqwMVtFVUAKrm6I7
NFHxxe2+4yNCbkhpCy4KbMoFS7x1mMB4myhSEMlWvzvAvwbIOeNHCmjI0BXcLrX3
mCnZbC74F/HZnksj75wQp+McBnlxru/9EcqhA8ogZ45sizGyk5kQVOiaDE3O3IVC
dATe9F7RaWn7JqQA74X+5COSw1GvShe2EmpO9J6p
=is8v
-----END PGP PUBLIC KEY BLOCK-----
`

func TestInspectGPGKey(t *testing.T) {
	if !commandAvailable("gpg") {
		t.Skip("gpg não disponível")
	}
	fingerprint, userId, err := inspectGPGKey([]byte(testFactoryKey))
	if err != nil {
		t.Fatalf("inspectGPGKey: %v", err)
	}
	const wantFingerprint = "AD485664E901B867051AB15F35A2F86E29B700A4"
	const wantUserId = "openSUSE Project Signing Key <opensuse@opensuse.org>"
	if fingerprint != wantFingerprint {
		t.Errorf("fingerprint = %q, want %q", fingerprint, wantFingerprint)
	}
	if userId != wantUserId {
		t.Errorf("userId = %q, want %q", userId, wantUserId)
	}
}

func TestInspectGPGKeyInvalid(t *testing.T) {
	if !commandAvailable("gpg") {
		t.Skip("gpg não disponível")
	}
	if _, _, err := inspectGPGKey([]byte("isto não é uma chave PGP")); err == nil {
		t.Fatalf("expected an error for invalid key data")
	}
}

func TestDnfRepoKeyURL(t *testing.T) {
	cases := map[string]string{
		"https://download.opensuse.org/repositories/home:/x/openSUSE_Leap_16.0":  "https://download.opensuse.org/repositories/home:/x/openSUSE_Leap_16.0/repodata/repomd.xml.key",
		"https://download.opensuse.org/repositories/home:/x/openSUSE_Leap_16.0/": "https://download.opensuse.org/repositories/home:/x/openSUSE_Leap_16.0/repodata/repomd.xml.key",
	}
	for in, want := range cases {
		if got := dnfRepoKeyURL(in); got != want {
			t.Errorf("dnfRepoKeyURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAptReleaseKeyURL(t *testing.T) {
	cases := map[string]string{
		"https://download.opensuse.org/repositories/home:/x/xUbuntu_24.04":  "https://download.opensuse.org/repositories/home:/x/xUbuntu_24.04/Release.key",
		"https://download.opensuse.org/repositories/home:/x/xUbuntu_24.04/": "https://download.opensuse.org/repositories/home:/x/xUbuntu_24.04/Release.key",
	}
	for in, want := range cases {
		if got := aptReleaseKeyURL(in); got != want {
			t.Errorf("aptReleaseKeyURL(%q) = %q, want %q", in, got, want)
		}
	}
}
