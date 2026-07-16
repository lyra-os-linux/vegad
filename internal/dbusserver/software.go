package dbusserver

import (
	"errors"
	"fmt"
	"log"
	"sync/atomic"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/distro"
)

// SoftwareService backs org.lyraos.Vega1.Software: unified search/install/update across the distro's package manager
// ("Oficial", via distro.PackageBackend), Flathub and the community layer
// ("Comunidade", via distro.CommunityBackend — nil on distros without one).
// Long transactions run in a goroutine and report progress via the
// TransactionProgress/TransactionFinished signals instead of the caller
// polling.
type SoftwareService struct {
	activity *Activity
	conn     *dbus.Conn
	provider distro.Provider
	nextTxID atomic.Uint32
}

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
func (s *SoftwareService) GetPackageDetails(origin, id string) (PackageDetails, *dbus.Error) {
	s.activity.Touch()

	var (
		details PackageDetails
		err     error
	)
	switch origin {
	case "official":
		details, err = s.provider.Package().GetDetails(id)
	case "flathub":
		details, err = fetchFlatpakDetails(id)
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
func (s *SoftwareService) Search(query string) ([]PackageRef, *dbus.Error) {
	s.activity.Touch()

	var results []PackageRef

	official, err := s.provider.Package().Search(query)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	results = append(results, official...)

	flathub, err := searchFlatpak(query)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	results = append(results, flathub...)

	if community := s.provider.Community(); community != nil {
		aur, err := community.Search(query)
		if err != nil {
			return nil, dbus.MakeFailedError(err)
		}
		results = append(results, aur...)
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
// receives a report func wired to TransactionProgress. why identifies the
// operation to systemd-logind for the duration of work, so a shutdown
// request doesn't cut a package transaction short (see withShutdownInhibit).
func (s *SoftwareService) startTransaction(why string, work func(report progressFunc) error) uint32 {
	txID := s.nextTxID.Add(1)
	report := func(percent uint32, message string) {
		if err := s.emitTransactionProgress(txID, percent, message); err != nil {
			log.Printf("vegad: emit TransactionProgress: %v", err)
		}
	}
	go func() {
		err := withShutdownInhibit(why, func() error { return work(report) })
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
		return s.startTransaction("Instalação oficial: "+id, func(report progressFunc) error {
			return withSnapshots("Instalação oficial: "+id, func() error {
				return s.provider.Package().Install(id, report)
			})
		}), nil
	case "flathub":
		return s.startTransaction("Instalação Flathub: "+id, func(report progressFunc) error { return installFlatpak(id, report) }), nil
	case "aur":
		community := s.provider.Community()
		if community == nil {
			return 0, dbus.MakeFailedError(errNoCommunityLayer())
		}
		return s.startTransaction("Instalação AUR: "+id, func(report progressFunc) error {
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
	if err := requirePolkit(sender, "org.lyraos.vega.software.remove"); err != nil {
		return 0, err
	}
	switch origin {
	case "official":
		return s.startTransaction("Remoção oficial: "+id, func(report progressFunc) error {
			return withSnapshots("Remoção oficial: "+id, func() error {
				return s.provider.Package().Remove(id, report)
			})
		}), nil
	case "flathub":
		return s.startTransaction("Remoção Flathub: "+id, func(report progressFunc) error { return removeFlatpak(id, report) }), nil
	case "aur":
		return s.startTransaction("Remoção AUR: "+id, func(report progressFunc) error {
			return withSnapshots("Remoção AUR: "+id, func() error {
				return s.provider.Package().Remove(id, report)
			})
		}), nil
	default:
		return 0, dbus.MakeFailedError(fmt.Errorf("origem desconhecida: %s", origin))
	}
}

// ListUpdates merges pending updates from the distro's package manager and
// Flatpak into one list.
func (s *SoftwareService) ListUpdates() ([]PackageRef, *dbus.Error) {
	s.activity.Touch()

	var results []PackageRef

	official, err := s.provider.Package().ListUpdates()
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	results = append(results, official...)

	flathub, err := listFlatpakUpdates()
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	results = append(results, flathub...)

	return results, nil
}

// ListInstalled merges locally installed distro packages and system Flatpak
// apps into one read-only list for the software inventory view.
func (s *SoftwareService) ListInstalled() ([]PackageRef, *dbus.Error) {
	s.activity.Touch()

	var results []PackageRef

	official, err := s.provider.Package().ListInstalled()
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	results = append(results, official...)

	flathub, err := listFlatpakInstalled()
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	results = append(results, flathub...)

	return results, nil
}

// UpdateAll runs a full sync+upgrade of the distro's package manager
// followed by a Flatpak update, as a single transaction covering both
// origins.
func (s *SoftwareService) UpdateAll(sender dbus.Sender) (uint32, *dbus.Error) {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.software.update"); err != nil {
		return 0, err
	}
	return s.startTransaction("Atualização completa", func(report progressFunc) error {
		if err := withSnapshots("Atualização completa", func() error {
			return s.provider.Package().UpdateAll(report)
		}); err != nil {
			return fmt.Errorf("%s: %w", s.provider.Package().Name(), err)
		}
		return updateAllFlatpak(report)
	}), nil
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

// OptimizeMirrors re-ranks package mirrors by download speed, where the
// active distro's PackageBackend supports it (returns distro.ErrUnsupported
// otherwise, e.g. openSUSE Leap).
func (s *SoftwareService) OptimizeMirrors(sender dbus.Sender) (uint32, *dbus.Error) {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.software.manage-repos"); err != nil {
		return 0, err
	}
	return s.startTransaction("Otimização de mirrors", func(report progressFunc) error {
		return s.provider.Package().OptimizeMirrors(report)
	}), nil
}

// ClearCache clears the distro package manager's cache and orphaned Flatpak
// runtimes as a single transaction.
func (s *SoftwareService) ClearCache(sender dbus.Sender) (uint32, *dbus.Error) {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.software.clear-cache"); err != nil {
		return 0, err
	}
	return s.startTransaction("Limpeza de cache", func(report progressFunc) error {
		if err := withSnapshots("Limpeza de cache", func() error {
			return s.provider.Package().ClearCache(report)
		}); err != nil {
			return fmt.Errorf("%s: %w", s.provider.Package().Name(), err)
		}
		if err := clearFlatpakCache(report); err != nil {
			return fmt.Errorf("flatpak: %w", err)
		}
		return nil
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

func (s *SoftwareService) emitTransactionFinished(txID uint32, success bool, message string) error {
	return s.conn.Emit(ObjectPath, BusName+".Software.TransactionFinished", txID, success, message)
}
