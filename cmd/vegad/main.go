// Command vegad is the privileged daemon behind Vega. It is bus-activated
// by systemd (Type=dbus, BusName=org.lyraos.Vega1) and exits after being
// idle — see internal/dbusserver for the exported interfaces and the
// idle-shutdown policy.
package main

import (
	"log"
	"os"

	"github.com/lyraos/vegad/internal/dbusserver"
	"github.com/lyraos/vegad/internal/version"
)

func main() {
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
