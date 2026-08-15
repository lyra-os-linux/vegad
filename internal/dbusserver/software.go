package dbusserver

import (
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/distro"
	"github.com/lyraos/vegad/internal/profile"
)

// SoftwareService backs org.lyraos.Vega1.Software: unified search/install/update across the distro's package manager
// ("Oficial", via distro.PackageBackend), Flathub and the community layer
// ("Comunidade", via distro.CommunityBackend — nil on distros without one).
// Long transactions run in a goroutine and report progress via the
// TransactionProgress/TransactionFinished signals instead of the caller
// polling.
type SoftwareService struct {
	activity        *Activity
	conn            *dbus.Conn
	provider        distro.Provider
	profile         profile.Profile
	nextTxID        atomic.Uint32
	nativeUpdatesMu sync.Mutex
	nativeUpdatesAt time.Time
	nativeUpdates   []PackageRef
	updateCheckMu   sync.Mutex
	updateChecking  bool
}

const nativeUpdateCacheTTL = 5 * time.Minute

func capabilityUnavailable(capability string) *dbus.Error {
	return dbus.NewError(BusName+".Error.CapabilityUnavailable", []interface{}{fmt.Sprintf("capacidade %s indisponível no perfil ativo", capability)})
}

func (s *SoftwareService) flatpakEnabled() bool { return s.profile == profile.Desktop }

// PackageRef identifies a package within one origin ("official", "flathub",
// "aur") so the UI can dedupe the same app found across origins.
type PackageRef = distro.PackageRef

// PackageDetails is the expanded view of a single package shown in the
// detail panel (issue #8) — unlike PackageRef, fetching this touches the
// network/AUR helper, so it's only requested on demand, never as part of a
// list.
type PackageDetails = distro.PackageDetails

// errNoCommunityLayer is returned for the "aur" origin on distros whose
// Provider.Community() is nil (no AUR-equivalent layer, e.g. openSUSE Leap).
func errNoCommunityLayer() error {
	return fmt.Errorf("esta distribuição não possui uma camada de pacotes da comunidade")
}

// PackageManagerName reports the active distro's official package manager
// label ("Pacman", "Zypper", ...) so the UI doesn't have to hardcode one —
// read-only, no polkit gate needed.
func (s *SoftwareService) PackageManagerName() (string, *dbus.Error) {
	s.activity.Touch()
	return s.provider.Package().Name(), nil
}

// CommunityLayerName reports the active distro's community package layer
// label ("AUR"), or "" on distros without one (Provider.Community() == nil,
// e.g. openSUSE Leap) — read-only, no polkit gate needed.
func (s *SoftwareService) CommunityLayerName() (string, *dbus.Error) {
	s.activity.Touch()
	community := s.provider.Community()
	if community == nil {
		return "", nil
	}
	return community.Name(), nil
}

// GetPackageDetails fetches the expanded metadata for one package — read-only
// (no polkit gate needed), same as Search/ListUpdates.
func (s *SoftwareService) GetPackageDetails(sender dbus.Sender, origin, id string) (PackageDetails, *dbus.Error) {
	s.activity.Touch()

	var (
		details PackageDetails
		err     error
	)
	switch origin {
	case "official":
		details, err = s.provider.Package().GetDetails(id)
	case "flathub":
		if !s.flatpakEnabled() {
			return PackageDetails{}, capabilityUnavailable("flatpak")
		}
		u := desktopUserOrNil(s.conn, sender, "GetPackageDetails")
		details, err = fetchFlatpakDetails(id, u)
	case "aur":
		community := s.provider.Community()
		if community == nil {
			return PackageDetails{}, dbus.MakeFailedError(errNoCommunityLayer())
		}
		details, err = community.GetDetails(id)
	default:
		return PackageDetails{}, dbus.MakeFailedError(fmt.Errorf("origem desconhecida: %s", origin))
	}
	if err != nil {
		return PackageDetails{}, dbus.MakeFailedError(err)
	}
	return details, nil
}

// Search queries the distro's local package sync databases and Flathub,
// merging both into one flat list — deduplication across origins
// is the UI's job, so it can offer the origin picker
// on the card. The community origin ("aur") is skipped on distros without
// one (Provider.Community() == nil), same as when no helper is installed.
func (s *SoftwareService) Search(sender dbus.Sender, query string) ([]PackageRef, *dbus.Error) {
	s.activity.Touch()

	var results []PackageRef

	official, err := s.provider.Package().Search(query)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	results = append(results, official...)

	if s.flatpakEnabled() {
		u := desktopUserOrNil(s.conn, sender, "Search")
		flathub, err := searchFlatpak(query, u)
		if err != nil {
			return nil, dbus.MakeFailedError(err)
		}
		results = append(results, flathub...)
	}

	if community := s.provider.Community(); community != nil {
		aur, err := community.Search(query)
		if err != nil {
			return nil, dbus.MakeFailedError(err)
		}
		results = append(results, aur...)
	}

	return results, nil
}

// SearchNative is the server-client variant: it never resolves a desktop
// user and never invokes Flatpak, even if the host runs the desktop profile.
func (s *SoftwareService) SearchNative(query string) ([]PackageRef, *dbus.Error) {
	s.activity.Touch()
	results, err := s.provider.Package().Search(query)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	if community := s.provider.Community(); community != nil {
		items, err := community.Search(query)
		if err != nil {
			return nil, dbus.MakeFailedError(err)
		}
		results = append(results, items...)
	}
	return results, nil
}

// GetAurPkgbuild fetches the PKGBUILD for an AUR package so the UI can show
// it for review before the user confirms an install — read-only (no polkit
// gate needed), same as Search/ListUpdates.
func (s *SoftwareService) GetAurPkgbuild(id string) (string, *dbus.Error) {
	s.activity.Touch()
	community := s.provider.Community()
	if community == nil {
		return "", dbus.MakeFailedError(errNoCommunityLayer())
	}
	pkgbuild, err := community.GetBuildScript(id)
	if err != nil {
		return "", dbus.MakeFailedError(err)
	}
	return pkgbuild, nil
}

// startTransaction allocates a transaction id and runs work in the
// background, translating its outcome into TransactionFinished. work
// receives a report func wired to TransactionProgress and a pkgReport func
// wired to PackageProgress (only Install/Remove/UpdateAll's zypper path
// actually calls the latter — every other flow just ignores it). why
// identifies the operation to systemd-logind for the duration of work, so a
// shutdown request doesn't cut a package transaction short (see
// withShutdownInhibit).
func (s *SoftwareService) startTransaction(why string, work func(report progressFunc, pkgReport packageProgressFunc) error) uint32 {
	txID := s.nextTxID.Add(1)
	report := func(percent uint32, message string) {
		if err := s.emitTransactionProgress(txID, percent, message); err != nil {
			log.Printf("vegad: emit TransactionProgress: %v", err)
		}
	}
	pkgReport := func(pkg string, phase distro.PackagePhase, percent uint32) {
		if err := s.emitPackageProgress(txID, pkg, phase, percent); err != nil {
			log.Printf("vegad: emit PackageProgress: %v", err)
		}
	}
	go func() {
		err := withShutdownInhibit(why, func() error { return work(report, pkgReport) })
		success := err == nil
		message := "Concluído"
		if err != nil {
			message = err.Error()
		}
		if emitErr := s.emitTransactionFinished(txID, success, message); emitErr != nil {
			log.Printf("vegad: emit TransactionFinished: %v", emitErr)
		}
	}()
	return txID
}

func (s *SoftwareService) Install(sender dbus.Sender, origin, id string) (uint32, *dbus.Error) {
	s.activity.Touch()
	if origin == "flathub" && !s.flatpakEnabled() {
		return 0, capabilityUnavailable("flatpak")
	}
	switch origin {
	case "official", "flathub", "aur":
		if err := requirePolkit(sender, "org.lyraos.vega.software.install"); err != nil {
			return 0, err
		}
	default:
		return 0, dbus.MakeFailedError(fmt.Errorf("origem desconhecida: %s", origin))
	}

	switch origin {
	case "official":
		return s.startTransaction("Instalação oficial: "+id, func(report progressFunc, pkgReport packageProgressFunc) error {
			return withSnapshots("Instalação oficial: "+id, func() error {
				return s.provider.Package().Install(id, report, pkgReport)
			})
		}), nil
	case "flathub":
		return s.startTransaction("Instalação Flathub: "+id, func(report progressFunc, _ packageProgressFunc) error { return installFlatpak(id, report) }), nil
	case "aur":
		community := s.provider.Community()
		if community == nil {
			return 0, dbus.MakeFailedError(errNoCommunityLayer())
		}
		return s.startTransaction("Instalação AUR: "+id, func(report progressFunc, _ packageProgressFunc) error {
			return withSnapshots("Instalação AUR: "+id, func() error {
				return community.Install(id, report)
			})
		}), nil
	default:
		return 0, dbus.MakeFailedError(fmt.Errorf("origem desconhecida: %s", origin))
	}
}

func (s *SoftwareService) Remove(sender dbus.Sender, origin, id string) (uint32, *dbus.Error) {
	s.activity.Touch()
	if origin == "flathub" && !s.flatpakEnabled() {
		return 0, capabilityUnavailable("flatpak")
	}
	if err := requirePolkit(sender, "org.lyraos.vega.software.remove"); err != nil {
		return 0, err
	}
	switch origin {
	case "official":
		return s.startTransaction("Remoção oficial: "+id, func(report progressFunc, pkgReport packageProgressFunc) error {
			return withSnapshots("Remoção oficial: "+id, func() error {
				return s.provider.Package().Remove(id, report, pkgReport)
			})
		}), nil
	case "flathub":
		u := desktopUserOrNil(s.conn, sender, "Remove")
		scope := "system"
		if apps, err := flatpakInstalledApps(u); err == nil {
			if app, ok := apps[id]; ok {
				scope = app.Scope
			}
		}
		return s.startTransaction("Remoção Flathub: "+id, func(report progressFunc, _ packageProgressFunc) error { return removeFlatpak(id, scope, u, report) }), nil
	case "aur":
		return s.startTransaction("Remoção AUR: "+id, func(report progressFunc, pkgReport packageProgressFunc) error {
			return withSnapshots("Remoção AUR: "+id, func() error {
				return s.provider.Package().Remove(id, report, pkgReport)
			})
		}), nil
	default:
		return 0, dbus.MakeFailedError(fmt.Errorf("origem desconhecida: %s", origin))
	}
}

// ListUpdates merges pending updates from the distro's package manager and
// Flatpak into one list.
func (s *SoftwareService) ListUpdates(sender dbus.Sender) ([]PackageRef, *dbus.Error) {
	s.activity.Touch()
	if !s.flatpakEnabled() {
		updates, err := s.provider.Package().ListUpdates()
		if err != nil {
			return nil, dbus.MakeFailedError(err)
		}
		return updates, nil
	}
	u := desktopUserOrNil(s.conn, sender, "ListUpdates")

	// Official and Flathub updates come from entirely separate subprocesses
	// (zypper/pacman vs. flatpak) — run them concurrently rather than
	// paying their combined latency in sequence, since this sits directly
	// in the dashboard's startup path.
	var official, flathub []PackageRef
	var officialErr, flathubErr error

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		official, officialErr = s.provider.Package().ListUpdates()
	}()
	go func() {
		defer wg.Done()
		flathub, flathubErr = listFlatpakUpdates(u)
	}()
	wg.Wait()

	if officialErr != nil {
		return nil, dbus.MakeFailedError(officialErr)
	}
	if flathubErr != nil {
		return nil, dbus.MakeFailedError(flathubErr)
	}

	results := append([]PackageRef{}, official...)
	results = append(results, flathub...)
	return results, nil
}

func (s *SoftwareService) ListNativeUpdates() ([]PackageRef, *dbus.Error) {
	s.activity.Touch()
	s.nativeUpdatesMu.Lock()
	defer s.nativeUpdatesMu.Unlock()
	if time.Since(s.nativeUpdatesAt) < nativeUpdateCacheTTL {
		return append([]PackageRef(nil), s.nativeUpdates...), nil
	}
	updates, err := s.provider.Package().ListUpdates()
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	s.nativeUpdates = append([]PackageRef(nil), updates...)
	s.nativeUpdatesAt = time.Now()
	status := UpdateStatus{
		CheckedAt:   s.nativeUpdatesAt.UTC().Format(time.RFC3339),
		Profile:     string(s.profile),
		NativeCount: uint32(len(updates)),
		TotalCount:  uint32(len(updates)),
	}
	if err := persistUpdateStatus(updateStatePath(), status); err != nil {
		log.Printf("vegad: persistir consulta nativa de atualizações: %v", err)
	}
	return append([]PackageRef(nil), updates...), nil
}

func (s *SoftwareService) GetUpdateStatus() (UpdateStatus, *dbus.Error) {
	s.activity.Touch()
	status, err := readUpdateStatus(updateStatePath())
	if os.IsNotExist(err) {
		return UpdateStatus{Profile: string(s.profile)}, nil
	}
	if err != nil {
		return UpdateStatus{}, dbus.MakeFailedError(err)
	}
	return status, nil
}

// RequestUpdateCheck returns immediately with the cached state. When that
// state is stale it starts one background refresh; concurrent administrative
// logins share the same refresh instead of synchronizing repositories once
// per client.
func (s *SoftwareService) RequestUpdateCheck() (UpdateStatus, *dbus.Error) {
	s.activity.Touch()
	status, err := readUpdateStatus(updateStatePath())
	if err != nil && !os.IsNotExist(err) {
		return UpdateStatus{}, dbus.MakeFailedError(err)
	}
	if status.Profile == "" {
		status.Profile = string(s.profile)
	}
	checkedAt, parseErr := time.Parse(time.RFC3339, status.CheckedAt)
	stale := parseErr != nil || time.Since(checkedAt) >= nativeUpdateCacheTTL

	s.updateCheckMu.Lock()
	if stale && !s.updateChecking {
		s.updateChecking = true
		status.InProgress = true
		status.Error = ""
		_ = persistUpdateStatus(updateStatePath(), status)
		go s.refreshUpdateStatus()
	} else if s.updateChecking {
		status.InProgress = true
	}
	s.updateCheckMu.Unlock()
	return status, nil
}

func (s *SoftwareService) refreshUpdateStatus() {
	status := UpdateStatus{
		CheckedAt:  time.Now().UTC().Format(time.RFC3339),
		Profile:    string(s.profile),
		InProgress: true,
	}
	if err := s.provider.Package().SyncDatabase(); err != nil {
		status.Error = err.Error()
	} else if updates, err := s.provider.Package().ListUpdates(); err != nil {
		status.Error = err.Error()
	} else {
		status.NativeCount = uint32(len(updates))
		status.TotalCount = status.NativeCount
		if s.profile == profile.Desktop {
			if flatpaks, err := listFlatpakUpdates(nil); err != nil {
				status.Error = err.Error()
			} else {
				status.FlatpakCount = uint32(len(flatpaks))
				status.TotalCount += status.FlatpakCount
			}
		}
	}
	status.InProgress = false
	previous, _ := readUpdateStatus(updateStatePath())
	if err := persistUpdateStatus(updateStatePath(), status); err != nil {
		log.Printf("vegad: persistir estado solicitado no login: %v", err)
	}
	s.updateCheckMu.Lock()
	s.updateChecking = false
	s.updateCheckMu.Unlock()
	if s.conn != nil && updateStatusChanged(previous, status) {
		if err := s.conn.Emit(ObjectPath, BusName+".Software.UpdateStateChanged", status); err != nil {
			log.Printf("vegad: emitir mudança do estado de atualizações: %v", err)
		}
		if updateResultChanged(previous, status) {
			if err := s.conn.Emit(ObjectPath, BusName+".Software.UpdatesAvailable", status.TotalCount); err != nil {
				log.Printf("vegad: emitir alerta de atualizações: %v", err)
			}
		}
	}
	log.Printf("vegad: consulta administrativa de atualizações concluída perfil=%s total=%d erro=%t", s.profile, status.TotalCount, status.Error != "")
}

// ListInstalled merges locally installed distro packages and system Flatpak
// apps into one read-only list for the software inventory view.
func (s *SoftwareService) ListInstalled(sender dbus.Sender) ([]PackageRef, *dbus.Error) {
	s.activity.Touch()

	var results []PackageRef

	official, err := s.provider.Package().ListInstalled()
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	results = append(results, official...)
	if !s.flatpakEnabled() {
		return results, nil
	}

	u := desktopUserOrNil(s.conn, sender, "ListInstalled")
	flathub, err := listFlatpakInstalled(u)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	results = append(results, flathub...)

	return results, nil
}

func (s *SoftwareService) ListNativeInstalled() ([]PackageRef, *dbus.Error) {
	s.activity.Touch()
	packages, err := s.provider.Package().ListInstalled()
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	return packages, nil
}

// UpdateAll runs a full sync+upgrade of the distro's package manager
// followed by a Flatpak update, as a single transaction covering both
// origins.
func (s *SoftwareService) UpdateAll(sender dbus.Sender) (uint32, *dbus.Error) {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.software.update"); err != nil {
		return 0, err
	}
	var u *desktopUser
	if s.flatpakEnabled() {
		u = desktopUserOrNil(s.conn, sender, "UpdateAll")
	}
	return s.startTransaction("Atualização completa", func(report progressFunc, pkgReport packageProgressFunc) error {
		if err := withSnapshots("Atualização completa", func() error {
			return s.provider.Package().UpdateAll(report, pkgReport)
		}); err != nil {
			return err
		}
		if s.flatpakEnabled() {
			return updateAllFlatpak(u, report)
		}
		return nil
	}), nil
}

func (s *SoftwareService) UpdateAllNative(sender dbus.Sender) (uint32, *dbus.Error) {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.software.update"); err != nil {
		return 0, err
	}
	return s.startTransaction("Atualização nativa completa", func(report progressFunc, pkgReport packageProgressFunc) error {
		return withSnapshots("Atualização nativa completa", func() error {
			return s.provider.Package().UpdateAll(report, pkgReport)
		})
	}), nil
}

// UpdatePackage updates a single pending update (see ListUpdates), letting
// the origin's own dependency resolver pull in whatever else that requires,
// instead of running the full UpdateAll.
func (s *SoftwareService) UpdatePackage(sender dbus.Sender, origin, id string) (uint32, *dbus.Error) {
	s.activity.Touch()
	if origin == "flathub" && !s.flatpakEnabled() {
		return 0, capabilityUnavailable("flatpak")
	}
	if err := requirePolkit(sender, "org.lyraos.vega.software.update"); err != nil {
		return 0, err
	}
	switch origin {
	case "official":
		return s.startTransaction("Atualização oficial: "+id, func(report progressFunc, pkgReport packageProgressFunc) error {
			return withSnapshots("Atualização oficial: "+id, func() error {
				return s.provider.Package().UpdatePackage(id, report, pkgReport)
			})
		}), nil
	case "flathub":
		u := desktopUserOrNil(s.conn, sender, "UpdatePackage")
		scope := "system"
		if apps, err := flatpakInstalledApps(u); err == nil {
			if app, ok := apps[id]; ok {
				scope = app.Scope
			}
		}
		return s.startTransaction("Atualização Flathub: "+id, func(report progressFunc, _ packageProgressFunc) error {
			return updateFlatpak(id, scope, u, report)
		}), nil
	default:
		return 0, dbus.MakeFailedError(fmt.Errorf("origem desconhecida: %s", origin))
	}
}

func (s *SoftwareService) ListRepos() ([]distro.RepositoryRef, *dbus.Error) {
	s.activity.Touch()
	repos, err := s.provider.Package().ListRepos()
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	return repos, nil
}

func (s *SoftwareService) SetRepoEnabled(sender dbus.Sender, repo string, enabled bool) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.software.manage-repos"); err != nil {
		return err
	}
	if err := s.provider.Package().SetRepoEnabled(repo, enabled); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

// ClearCache clears the distro package manager's cache and orphaned Flatpak
// runtimes as a single transaction.
func (s *SoftwareService) ClearCache(sender dbus.Sender) (uint32, *dbus.Error) {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.software.clear-cache"); err != nil {
		return 0, err
	}
	var u *desktopUser
	if s.flatpakEnabled() {
		u = desktopUserOrNil(s.conn, sender, "ClearCache")
	}
	return s.startTransaction("Limpeza de cache", func(report progressFunc, _ packageProgressFunc) error {
		if err := withSnapshots("Limpeza de cache", func() error {
			return s.provider.Package().ClearCache(report)
		}); err != nil {
			return fmt.Errorf("%s: %w", s.provider.Package().Name(), err)
		}
		if s.flatpakEnabled() {
			if err := clearFlatpakCache(u, report); err != nil {
				return fmt.Errorf("flatpak: %w", err)
			}
		}
		return nil
	}), nil
}

func (s *SoftwareService) ClearNativeCache(sender dbus.Sender) (uint32, *dbus.Error) {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.software.clear-cache"); err != nil {
		return 0, err
	}
	return s.startTransaction("Limpeza de cache nativo", func(report progressFunc, _ packageProgressFunc) error {
		return withSnapshots("Limpeza de cache nativo", func() error {
			return s.provider.Package().ClearCache(report)
		})
	}), nil
}

func withSnapshots(action string, work func() error) error {
	preID, preErr := createSnapperSnapshot("pre", action)
	if preErr != nil && !errors.Is(preErr, errSnapperUnavailable) {
		log.Printf("vegad: snapshot pre falhou (%s): %v", action, preErr)
	} else if preErr == nil {
		log.Printf("vegad: snapshot pre criado (%s): %d", action, preID)
	}

	err := work()

	if preErr == nil {
		if postID, postErr := createSnapperSnapshot("post", action, preID); postErr != nil {
			if !errors.Is(postErr, errSnapperUnavailable) {
				log.Printf("vegad: snapshot post falhou (%s): %v", action, postErr)
			}
		} else {
			log.Printf("vegad: snapshot post criado (%s): %d", action, postID)
		}
	}

	return err
}

func (s *SoftwareService) emitTransactionProgress(txID uint32, percent uint32, message string) error {
	return s.conn.Emit(ObjectPath, BusName+".Software.TransactionProgress", txID, percent, message)
}

func (s *SoftwareService) emitPackageProgress(txID uint32, pkg string, phase distro.PackagePhase, percent uint32) error {
	return s.conn.Emit(ObjectPath, BusName+".Software.PackageProgress", txID, pkg, string(phase), percent)
}

func (s *SoftwareService) emitTransactionFinished(txID uint32, success bool, message string) error {
	return s.conn.Emit(ObjectPath, BusName+".Software.TransactionFinished", txID, success, message)
}

func (s *SoftwareService) emitRepoKeyPending(txID uint32, repo, keyId, fingerprint, userId string) error {
	return s.conn.Emit(ObjectPath, BusName+".Software.RepoKeyPending", txID, repo, keyId, fingerprint, userId)
}

// startTransactionWithID is startTransaction's twin for the one case where
// work needs the transaction id before it finishes: AddRepo/TrustRepoKey
// emit an extra RepoKeyPending signal (tagged with this same txID) when the
// repo's signing key still needs the user's approval, ahead of the
// TransactionFinished that follows either way.
func (s *SoftwareService) startTransactionWithID(why string, work func(txID uint32, report progressFunc) error) uint32 {
	txID := s.nextTxID.Add(1)
	report := func(percent uint32, message string) {
		if err := s.emitTransactionProgress(txID, percent, message); err != nil {
			log.Printf("vegad: emit TransactionProgress: %v", err)
		}
	}
	go func() {
		err := withShutdownInhibit(why, func() error { return work(txID, report) })
		success := err == nil
		message := "Concluído"
		if err != nil {
			message = err.Error()
		}
		if emitErr := s.emitTransactionFinished(txID, success, message); emitErr != nil {
			log.Printf("vegad: emit TransactionFinished: %v", emitErr)
		}
	}()
	return txID
}

// AddRepo registers a new repository (name pointing at url) and tries to
// refresh it. When the repo is signed by a key the backend doesn't trust
// yet, the transaction still finishes (success=false) but a RepoKeyPending
// signal fires first with enough detail (keyId/fingerprint/userId) for the
// UI to ask the user whether to trust it via TrustRepoKey — see
// distro.UntrustedKeyError.
func (s *SoftwareService) AddRepo(sender dbus.Sender, name, url string) (uint32, *dbus.Error) {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.software.manage-repos"); err != nil {
		return 0, err
	}
	return s.startTransactionWithID("Adicionar repositório: "+name, func(txID uint32, report progressFunc) error {
		err := s.provider.Package().AddRepo(name, url, report)
		var keyErr *distro.UntrustedKeyError
		if errors.As(err, &keyErr) {
			if emitErr := s.emitRepoKeyPending(txID, keyErr.Repo, keyErr.KeyId, keyErr.Fingerprint, keyErr.UserId); emitErr != nil {
				log.Printf("vegad: emit RepoKeyPending: %v", emitErr)
			}
		}
		return err
	}), nil
}

// TrustRepoKey imports/trusts keyId (as previously reported via a
// RepoKeyPending signal for repo) and retries refreshing the repository.
func (s *SoftwareService) TrustRepoKey(sender dbus.Sender, repo, keyId string) (uint32, *dbus.Error) {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.software.manage-repos"); err != nil {
		return 0, err
	}
	return s.startTransaction("Confiar em chave de repositório: "+repo, func(report progressFunc, _ packageProgressFunc) error {
		return s.provider.Package().TrustRepoKey(repo, keyId, report)
	}), nil
}
