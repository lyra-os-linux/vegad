package distro

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"
)

// debianHardwareBackend drives Ubuntu's NVIDIA driver packages through
// ubuntu-drivers (package ubuntu-drivers-common, present on the standard
// desktop ISO), rather than hardcoding a driver version number the way
// openSUSEHardwareBackend does for its G06 packages — NVIDIA driver
// version numbers age out of an Ubuntu release's repos far faster than
// this code would get updated, whereas ubuntu-drivers always resolves to
// whatever the current recommended package is. This has not been
// exercised against a live Ubuntu install with a real NVIDIA GPU; treat it
// as a documented starting point to confirm, same caveat
// hardware_opensuse.go already carries.
type debianHardwareBackend struct{}

func newDebianHardwareBackend() *debianHardwareBackend { return &debianHardwareBackend{} }

// debianDriverPackage matches one "nvidia-driver-NNN[-server]" token from
// `ubuntu-drivers list --gpgpu` / `ubuntu-drivers devices` output.
var debianDriverPackage = regexp.MustCompile(`nvidia-driver-[0-9]+(-server)?`)

func (h *debianHardwareBackend) AvailableNvidiaDrivers() []string {
	drivers := []string{}
	seen := map[string]bool{}

	if commandAvailable("ubuntu-drivers") {
		out, err := runCommandOutput("ubuntu-drivers", "list", "--gpgpu")
		if err != nil {
			// --gpgpu is only meaningful on GPGPU-capable hardware/newer
			// ubuntu-drivers versions; fall back to the plain listing.
			out, _ = runCommandOutput("ubuntu-drivers", "list")
		}
		scanner := bufio.NewScanner(strings.NewReader(out))
		for scanner.Scan() {
			for _, match := range debianDriverPackage.FindAllString(scanner.Text(), -1) {
				if !seen[match] {
					seen[match] = true
					drivers = append(drivers, match)
				}
			}
		}
	}

	return append(drivers, "nouveau")
}

func (h *debianHardwareBackend) SwitchNvidiaDriver(driver string, report ProgressFunc) error {
	installed, err := aptInstalledSet()
	if err != nil {
		return err
	}

	var removePackages []string
	for name := range installed {
		if debianDriverPackage.MatchString(name) && name != driver {
			removePackages = append(removePackages, name)
		}
	}

	for _, pkg := range removePackages {
		cmd := aptGetCommand("remove", "-y", "--", pkg)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("apt-get remove %s: %w — %s", pkg, err, strings.TrimSpace(string(out)))
		}
	}

	if driver != "nouveau" && !installed[driver] {
		if err := runAptGet([]string{"install", "-y", "--", driver}, report,
			"Instalando "+driver+"...", "Concluído"); err != nil {
			return err
		}
	}

	return rebuildDebianBootArtifacts()
}
