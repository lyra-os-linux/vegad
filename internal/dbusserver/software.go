package dbusserver

import (
	"errors"
	"fmt"
	"log"
	"sync/atomic"

	"github.com/godbus/dbus/v5"
)

// SoftwareService backs org.lyraos.Vega1.Software (PROMPT-VEGA.md §3.1):
// unified search/install/update across Pacman ("Oficial"), Flathub and AUR
// ("Comunidade"). Long transactions run in a goroutine and report progress
// via the TransactionProgress/TransactionFinished signals instead of the
// caller polling.
type SoftwareService struct {
	activity *Activity
	conn     *dbus.Conn
	nextTxID atomic.Uint32
}

// PackageRef identifies a package within one origin ("official", "flathub",
// "aur") so the UI can dedupe the same app found across origins.
type PackageRef struct {
	Origin      string
	Id          string
	Name        string
	Description string
	Installed   bool
}

// PackageDetails is the expanded view of a single package shown in the
// detail panel (issue #8) — unlike PackageRef, fetching this touches the
// network/AUR helper, so it's only requested on demand, never as part of a
// list.
type PackageDetails struct {
	Origin           string
	Id               string
	Name             string
	Description      string
	Installed        bool
	InstalledVersion string
	AvailableVersion string
	DownloadSize     string
	InstalledSize    string
	Dependencies     []string
	Licenses         []string
	URL              string
	Maintainer       string
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
		details, err = fetchPacmanDetails(id)
	case "flathub":
		details, err = fetchFlatpakDetails(id)
	case "aur":
		details, err = fetchAurDetails(id)
	default:
		return PackageDetails{}, dbus.MakeFailedError(fmt.Errorf("origem desconhecida: %s", origin))
	}
	if err != nil {
		return PackageDetails{}, dbus.MakeFailedError(err)
	}
	return details, nil
}

// Search queries Pacman's local sync databases and Flathub, merging both
// into one flat list — deduplication across origins (PROMPT-VEGA.md §3.1)
// is the UI's job, so it can offer the origin picker on the card. AUR
// ("Comunidade") search isn't wired up yet.
func (s *SoftwareService) Search(query string) ([]PackageRef, *dbus.Error) {
	s.activity.Touch()

	var results []PackageRef

	official, err := searchPacman(query)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	results = append(results, official...)

	flathub, err := searchFlatpak(query)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	results = append(results, flathub...)

	aur, err := searchAur(query)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	results = append(results, aur...)

	return results, nil
}

// GetAurPkgbuild fetches the PKGBUILD for an AUR package so the UI can show
// it for review before the user confirms an install — read-only (no polkit
// gate needed), same as Search/ListUpdates.
func (s *SoftwareService) GetAurPkgbuild(id string) (string, *dbus.Error) {
	s.activity.Touch()
	pkgbuild, err := fetchAurPkgbuild(id)
	if err != nil {
		return "", dbus.MakeFailedError(err)
	}
	return pkgbuild, nil
}

// startTransaction allocates a transaction id and runs work in the
// background, translating its outcome into TransactionFinished. work
// receives a report func wired to TransactionProgress.
func (s *SoftwareService) startTransaction(work func(report progressFunc) error) uint32 {
	txID := s.nextTxID.Add(1)
	report := func(percent uint32, message string) {
		if err := s.emitTransactionProgress(txID, percent, message); err != nil {
			log.Printf("vegad: emit TransactionProgress: %v", err)
		}
	}
	go func() {
		err := work(report)
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
	case "official":
		if err := requirePolkit(sender, "org.lyraos.vega.software.install"); err != nil {
			return 0, err
		}
	case "flathub":
		if err := requirePolkit(sender, "org.lyraos.vega.software.install"); err != nil {
			return 0, err
		}
	case "aur":
		if err := requirePolkit(sender, "org.lyraos.vega.software.install"); err != nil {
			return 0, err
		}
	default:
		return 0, dbus.MakeFailedError(fmt.Errorf("origem desconhecida: %s", origin))
	}

	switch origin {
	case "official":
		return s.startTransaction(func(report progressFunc) error {
			return withPacmanSnapshots("Instalação oficial: "+id, func() error {
				return installPacman(id, report)
			})
		}), nil
	case "flathub":
		return s.startTransaction(func(report progressFunc) error { return installFlatpak(id, report) }), nil
	case "aur":
		return s.startTransaction(func(report progressFunc) error {
			return withPacmanSnapshots("Instalação AUR: "+id, func() error {
				return installAurPackage(id, report)
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
		return s.startTransaction(func(report progressFunc) error {
			return withPacmanSnapshots("Remoção oficial: "+id, func() error {
				return removePacman(id, report)
			})
		}), nil
	case "flathub":
		return s.startTransaction(func(report progressFunc) error { return removeFlatpak(id, report) }), nil
	case "aur":
		return s.startTransaction(func(report progressFunc) error {
			return withPacmanSnapshots("Remoção AUR: "+id, func() error {
				return removePacman(id, report)
			})
		}), nil
	default:
		return 0, dbus.MakeFailedError(fmt.Errorf("origem desconhecida: %s", origin))
	}
}

// ListUpdates merges pending Pacman and Flatpak updates into one list.
func (s *SoftwareService) ListUpdates() ([]PackageRef, *dbus.Error) {
	s.activity.Touch()

	var results []PackageRef

	official, err := listPacmanUpdates()
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

// UpdateAll runs a full Pacman sync+upgrade followed by a Flatpak update, as
// a single transaction covering both origins.
func (s *SoftwareService) UpdateAll(sender dbus.Sender) (uint32, *dbus.Error) {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.software.update"); err != nil {
		return 0, err
	}
	return s.startTransaction(func(report progressFunc) error {
		if err := withPacmanSnapshots("Atualização completa", func() error {
			return updateAllPacman(report)
		}); err != nil {
			return fmt.Errorf("pacman: %w", err)
		}
		return updateAllFlatpak(report)
	}), nil
}

func (s *SoftwareService) ListRepos() ([]string, *dbus.Error) {
	s.activity.Touch()
	repos, err := listPacmanRepos()
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
	if err := setPacmanRepoEnabled(repo, enabled); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

// ClearCache clears Pacman's package cache and orphaned Flatpak runtimes as
// a single transaction.
func (s *SoftwareService) ClearCache(sender dbus.Sender) (uint32, *dbus.Error) {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.software.clear-cache"); err != nil {
		return 0, err
	}
	return s.startTransaction(func(report progressFunc) error {
		if err := withPacmanSnapshots("Limpeza de cache", func() error {
			return clearPacmanCache(report)
		}); err != nil {
			return fmt.Errorf("pacman: %w", err)
		}
		if err := clearFlatpakCache(report); err != nil {
			return fmt.Errorf("flatpak: %w", err)
		}
		return nil
	}), nil
}

func withPacmanSnapshots(action string, work func() error) error {
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
