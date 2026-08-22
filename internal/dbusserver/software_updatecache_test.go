package dbusserver

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/lyraos/vegad/internal/distro"
	"github.com/lyraos/vegad/internal/profile"
)

// countingPackageBackend records how often the pending-update query actually
// reached the package manager. Only the methods these tests exercise are
// defined; the embedded interface leaves the rest nil so an unexpected call
// fails loudly instead of silently passing.
type countingPackageBackend struct {
	distro.PackageBackend
	mu             sync.Mutex
	calls          int
	installedCalls int
	updates        []distro.PackageRef
	installed      []distro.PackageRef
}

func (b *countingPackageBackend) ListUpdates() ([]distro.PackageRef, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	return append([]distro.PackageRef(nil), b.updates...), nil
}

func (b *countingPackageBackend) ListInstalled() ([]distro.PackageRef, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.installedCalls++
	return append([]distro.PackageRef(nil), b.installed...), nil
}

func (b *countingPackageBackend) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

type countingProvider struct {
	distro.Provider
	pkg *countingPackageBackend
}

func (p *countingProvider) Package() distro.PackageBackend { return p.pkg }

func newCachingService(t *testing.T, p profile.Profile) (*SoftwareService, *countingPackageBackend) {
	t.Helper()
	t.Setenv("VEGAD_UPDATE_STATE", filepath.Join(t.TempDir(), "update-status.json"))
	backend := &countingPackageBackend{
		updates:   []distro.PackageRef{{Origin: "official", Id: "vim", Name: "vim"}},
		installed: []distro.PackageRef{{Origin: "official", Id: "bash", Name: "bash"}},
	}
	return &SoftwareService{
		activity: &Activity{},
		provider: &countingProvider{pkg: backend},
		profile:  p,
	}, backend
}

// The Updates tab re-reads the list on every click; before the cache that
// meant a full zypper run each time.
func TestListUpdatesServesRepeatCallsFromCache(t *testing.T) {
	svc, backend := newCachingService(t, profile.Server)

	for i := 0; i < 3; i++ {
		got, err := svc.ListUpdates("")
		if err != nil {
			t.Fatalf("chamada %d: %v", i, err)
		}
		if len(got) != 1 || got[0].Id != "vim" {
			t.Fatalf("chamada %d devolveu %#v", i, got)
		}
	}
	if calls := backend.callCount(); calls != 1 {
		t.Fatalf("gerenciador de pacotes consultado %d vezes, esperado 1", calls)
	}
}

// The dashboard and the Updates tab ask different methods for the same
// answer, so they must share one cache rather than each paying for it.
func TestNativeAndMergedListsShareOneCache(t *testing.T) {
	svc, backend := newCachingService(t, profile.Server)

	if _, err := svc.ListNativeUpdates(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListUpdates(""); err != nil {
		t.Fatal(err)
	}
	if calls := backend.callCount(); calls != 1 {
		t.Fatalf("gerenciador de pacotes consultado %d vezes, esperado 1", calls)
	}
}

// Without this the tab keeps listing packages the user already installed for
// up to the cache TTL.
func TestInvalidateForcesFreshRead(t *testing.T) {
	svc, backend := newCachingService(t, profile.Server)

	if _, err := svc.ListUpdates(""); err != nil {
		t.Fatal(err)
	}
	svc.invalidateUpdateCaches()
	if _, err := svc.ListUpdates(""); err != nil {
		t.Fatal(err)
	}
	if calls := backend.callCount(); calls != 2 {
		t.Fatalf("gerenciador de pacotes consultado %d vezes, esperado 2", calls)
	}
}

// The dashboard refresh and a tab click can land together at startup; the
// second must wait for the first rather than starting its own zypper run.
func TestConcurrentReadersCollapseIntoOneQuery(t *testing.T) {
	svc, backend := newCachingService(t, profile.Server)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.ListUpdates(""); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls := backend.callCount(); calls != 1 {
		t.Fatalf("gerenciador de pacotes consultado %d vezes, esperado 1", calls)
	}
}

// A cached list built from one desktop user's --user Flatpak installation
// must never be handed to another.
func TestFlatpakScopeKeySeparatesUsers(t *testing.T) {
	if flatpakScopeKey(nil) != "" {
		t.Fatal("escopo apenas de sistema deve ser a chave vazia")
	}
	if flatpakScopeKey(&desktopUser{Uid: 1000}) == flatpakScopeKey(&desktopUser{Uid: 1001}) {
		t.Fatal("usuários distintos devem gerar chaves distintas")
	}
}

// ListInstalled was restructured so the native and Flatpak queries overlap;
// the profile without Flatpak must still take the native-only path and never
// reach the concurrent branch.
func TestListInstalledWithoutFlatpakReturnsNativeOnly(t *testing.T) {
	svc, backend := newCachingService(t, profile.Server)

	got, err := svc.ListInstalled("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Id != "bash" {
		t.Fatalf("instalados = %#v", got)
	}
	if backend.installedCalls != 1 {
		t.Fatalf("gerenciador consultado %d vezes, esperado 1", backend.installedCalls)
	}
}
