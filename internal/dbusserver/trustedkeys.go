package dbusserver

import (
	"log"
	"os"

	"github.com/lyraos/vegad/internal/distro"
	"github.com/lyraos/vegad/internal/profile"
)

// RunTrustedKeysJob applies the shipped trust policy independently of first
// boot completion/exemption. It never refreshes repositories or installs RPMs.
func RunTrustedKeysJob(activeProfile profile.Profile) error {
	if activeProfile != profile.Desktop {
		return nil
	}
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return err
	}
	return maintainTrustedKeys(activeProfile, string(cmdline), func() error {
		return distro.ImportTrustedPackageKeys(trustedKeyringPath())
	})
}

func maintainTrustedKeys(activeProfile profile.Profile, cmdline string, importKeys func() error) error {
	if activeProfile != profile.Desktop || isLiveCmdline(cmdline) {
		return nil
	}
	if err := importKeys(); err != nil {
		return err
	}
	log.Printf("vegad: chaves de assinatura autorizadas sincronizadas")
	return nil
}
