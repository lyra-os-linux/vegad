package dbusserver

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lyraos/vegad/internal/distro"
)

// A repository whose key is still pending is reported in words, without the
// fingerprint: RefreshRepos never trusts a key. Other failures pass through
// with the distro layer's explanation.
func TestRefreshReposError(t *testing.T) {
	key := &distro.UntrustedKeyError{Repo: "code", KeyId: "BC528686B50D79E339D3721CEB3E94ADBE1229CF", Fingerprint: "BC52 8686 B50D 79E3 39D3 721C EB3E 94AD BE12 29CF", UserId: "Microsoft"}
	err := refreshReposError(fmt.Errorf("refresh: %w", key))
	if err == nil || strings.Contains(err.Error(), "BC52") || !strings.Contains(err.Error(), "chave de assinatura que ainda não foi aprovada") {
		t.Fatalf("untrusted key: %v", err)
	}
	other := errors.New("Não foi possível acessar o repositório repo-oss: o servidor não respondeu.")
	if got := refreshReposError(other); got != other {
		t.Fatalf("other failure was rewritten: %v", got)
	}
}
