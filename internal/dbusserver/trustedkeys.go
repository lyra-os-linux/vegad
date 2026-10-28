package dbusserver

import (
	"context"
	"log"
	"os"
	"time"

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
	ctx, cancel := context.WithTimeout(context.Background(), trustedKeysJobTimeout)
	defer cancel()
	return maintainTrustedKeys(activeProfile, string(cmdline), func() error {
		return distro.ImportTrustedPackageKeysContext(ctx, trustedKeyringPath())
	})
}

// trustedKeysJobTimeout bounds how long the job waits for an RPM transaction
// that holds the database lock, such as the Zypper update installing vegad.
// It must stay below TimeoutStartSec of vegad-trusted-keys.service.
const trustedKeysJobTimeout = 25 * time.Minute

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
