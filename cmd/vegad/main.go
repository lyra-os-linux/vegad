// Command vegad is the privileged daemon behind Vega. It is bus-activated
// by systemd (Type=dbus, BusName=org.lyraos.Vega1) and exits after being
// idle — see internal/dbusserver for the exported interfaces and the
// idle-shutdown policy.
package main

import (
	"log"
	"os"
	"strings"

	"github.com/lyraos/vegad/internal/dbusserver"
	"github.com/lyraos/vegad/internal/version"
)

// defaultSystemPATH covers where every CLI vegad shells out to (apt, df,
// dpkg-query, journalctl, systemctl, snapper/timeshift, firewall-cmd/ufw,
// ...) normally lives. systemd's Type=dbus activation grants services its
// own sane default PATH, but on SysVinit distros (e.g. MX Linux) the system
// bus activates org.lyraos.Vega1 by having the classic dbus-daemon fork+exec
// the binary directly (see packaging/vegad/org.lyraos.Vega1.service's
// Exec=), inheriting dbus-daemon's own minimal boot-time environment —
// which can have PATH unset entirely, breaking every exec.Command call in
// this daemon at once.
const defaultSystemPATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

func ensureSystemPATH() {
	current := os.Getenv("PATH")
	if current == "" {
		os.Setenv("PATH", defaultSystemPATH)
		return
	}

	have := make(map[string]bool)
	for _, dir := range strings.Split(current, ":") {
		have[dir] = true
	}
	for _, dir := range strings.Split(defaultSystemPATH, ":") {
		if !have[dir] {
			current += ":" + dir
			have[dir] = true
		}
	}
	os.Setenv("PATH", current)
}

func main() {
	ensureSystemPATH()

	if len(os.Args) >= 2 && os.Args[1] == "check-updates" {
		if err := dbusserver.RunUpdateCheckJob(); err != nil {
			log.Fatalf("vegad check-updates failed: %v", err)
		}
		return
	}

	if len(os.Args) >= 4 && os.Args[1] == "backup" && os.Args[2] == "run" {
		configID := os.Args[3]
		log.Printf("vegad backup job %s starting", configID)
		err := dbusserver.WithShutdownInhibit("Backup: "+configID, func() error {
			return dbusserver.RunBackupJob(configID, func(percent uint32, message string) {
				log.Printf("backup %s: %d%% %s", configID, percent, message)
			})
		})
		if err != nil {
			log.Fatalf("vegad backup job %s failed: %v", configID, err)
		}
		log.Printf("vegad backup job %s finished", configID)
		return
	}

	log.Printf("vegad %s starting", version.Version)

	srv, err := dbusserver.New()
	if err != nil {
		log.Fatalf("vegad: connect to system bus: %v", err)
	}
	defer srv.Close()

	if err := srv.Export(); err != nil {
		log.Fatalf("vegad: export interfaces: %v", err)
	}

	log.Printf("vegad: exported %s at %s", dbusserver.BusName, dbusserver.ObjectPath)
	srv.Run()
	log.Printf("vegad: shutting down")
}
