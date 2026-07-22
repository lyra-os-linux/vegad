package distro

import "testing"

func TestPacmanUnknownTrustRe(t *testing.T) {
	line := `error: home_rodrigosbrito_vega: signature from "Rodrigo Brito <rodrigo@w3ti.com.br>" is unknown trust`
	m := pacmanUnknownTrustRe.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("expected a match")
	}
	if m[1] != "Rodrigo Brito <rodrigo@w3ti.com.br>" {
		t.Errorf("captured name = %q", m[1])
	}
}

func TestPacmanUnknownTrustReUnrelatedError(t *testing.T) {
	if m := pacmanUnknownTrustRe.FindStringSubmatch("error: failed retrieving file 'home_rodrigosbrito_vega.db'"); m != nil {
		t.Fatalf("expected no match for an unrelated error, got %+v", m)
	}
}
