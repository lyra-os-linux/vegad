package dbusserver

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/godbus/dbus/v5"
)

// KernelService backs org.lyraos.Vega1.Kernel (PROMPT-VEGA.md §3.4):
// switches between linux-zen and linux-lts, regenerating GRUB. Must never
// remove the running kernel or leave the system with zero kernels.
type KernelService struct {
	activity *Activity
	conn     *dbus.Conn
	nextTxID atomic.Uint32
}

func (k *KernelService) ListInstalled() ([]string, *dbus.Error) {
	k.activity.Touch()
	installed, err := pacmanInstalledSet()
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}

	var kernels []string
	for _, kernel := range []string{"linux", "linux-lts", "linux-zen"} {
		if installed[kernel] {
			kernels = append(kernels, kernel)
		}
	}
	return kernels, nil
}

func (k *KernelService) Install(kernel string) (uint32, *dbus.Error) {
	k.activity.Touch()
	if !isSupportedKernelPackage(kernel) {
		return 0, dbus.MakeFailedError(fmt.Errorf("kernel inválido: %s", kernel))
	}

	txID := k.nextTxID.Add(1)
	go func() {
		err := withPacmanSnapshots("Instalação de kernel: "+kernel, func() error {
			return runPacmanTransaction([]string{"-S", "--noconfirm", "--", kernel}, func(uint32, string) {})
		})
		if err == nil {
			err = rebuildBootArtifacts()
		}
		if err != nil {
			logKernelError("install", kernel, err)
		}
	}()
	return txID, nil
}

func (k *KernelService) Remove(kernel string) *dbus.Error {
	k.activity.Touch()
	if !isSupportedKernelPackage(kernel) {
		return dbus.MakeFailedError(fmt.Errorf("kernel inválido: %s", kernel))
	}
	if runningKernelMatches(kernel) {
		return dbus.MakeFailedError(fmt.Errorf("não é permitido remover o kernel em execução (%s)", kernel))
	}

	installed, err := pacmanInstalledSet()
	if err != nil {
		return dbus.MakeFailedError(err)
	}
	count := 0
	for _, candidate := range []string{"linux", "linux-lts", "linux-zen"} {
		if installed[candidate] {
			count++
		}
	}
	if count <= 1 && installed[kernel] {
		return dbus.MakeFailedError(fmt.Errorf("não é permitido deixar o sistema sem kernel instalado"))
	}

	if !installed[kernel] {
		return nil
	}

	if err := withPacmanSnapshots("Remoção de kernel: "+kernel, func() error {
		return runPacmanTransaction([]string{"-R", "--noconfirm", "--", kernel}, func(uint32, string) {})
	}); err != nil {
		return dbus.MakeFailedError(err)
	}
	if err := rebuildBootArtifacts(); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func isSupportedKernelPackage(kernel string) bool {
	switch kernel {
	case "linux", "linux-lts", "linux-zen":
		return true
	default:
		return false
	}
}

func runningKernelMatches(kernel string) bool {
	out, err := runCommandOutput("uname", "-r")
	if err != nil {
		return false
	}
	switch kernel {
	case "linux-zen":
		return strings.Contains(out, "zen")
	case "linux-lts":
		return strings.Contains(out, "lts")
	default:
		return !strings.Contains(out, "zen") && !strings.Contains(out, "lts")
	}
}

func rebuildBootArtifacts() error {
	if commandAvailable("mkinitcpio") {
		if err := runCommand("mkinitcpio", "-P"); err != nil {
			return err
		}
	}
	if commandAvailable("grub-mkconfig") {
		if err := runCommand("grub-mkconfig", "-o", "/boot/grub/grub.cfg"); err != nil {
			return err
		}
	}
	return nil
}

func logKernelError(action, kernel string, err error) {
	fmt.Printf("vegad: kernel %s %s failed: %v\n", action, kernel, err)
}
