// Command vegad is the privileged daemon behind Vega. It is bus-activated
// by systemd (Type=dbus, BusName=org.lyraos.Vega1) and exits after being
// idle — see internal/dbusserver for the exported interfaces and the
// idle-shutdown policy.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lyraos/vegad/internal/dbusserver"
	"github.com/lyraos/vegad/internal/profile"
	"github.com/lyraos/vegad/internal/version"
)

// defaultSystemPATH covers where every CLI vegad shells out to (apt, df,
// dpkg-query, journalctl, systemctl, snapper, firewall-cmd/ufw, ...)
// normally lives. systemd's Type=dbus activation grants services its
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
	activeProfile, profileSource, err := profile.Load(profile.DefaultConfigPath)
	if err != nil {
		log.Fatalf("vegad: carregar perfil: %v", err)
	}
	log.Printf("vegad: perfil %s (%s)", activeProfile, profileSource)
	if len(os.Args) == 2 && os.Args[1] == "query" {
		if err := dbusserver.RunQueryWorker(activeProfile, os.Stdin, os.Stdout); err != nil {
			log.Fatalf("vegad query: %v", err)
		}
		return
	}

	if len(os.Args) == 2 && os.Args[1] == "check-updates" {
		if activeProfile != profile.Desktop {
			log.Printf("vegad: verificação periódica ignorada no perfil %s", activeProfile)
			return
		}
		if err := dbusserver.RunUpdateCheckJob(activeProfile); err != nil {
			log.Fatalf("vegad check-updates failed: %v", err)
		}
		return
	}

	if len(os.Args) == 2 && os.Args[1] == "check-health" {
		// Desktop and Server: the Server shows the result in vega-cli.
		if err := dbusserver.RunHealthCheck(); err != nil {
			log.Fatalf("vegad check-health failed: %v", err)
		}
		return
	}

	if len(os.Args) == 2 && os.Args[1] == "sync-trusted-keys" {
		if err := dbusserver.RunTrustedKeysJob(activeProfile); err != nil {
			log.Fatalf("vegad sync-trusted-keys failed: %v", err)
		}
		return
	}

	if len(os.Args) == 2 && os.Args[1] == "first-update" {
		if activeProfile != profile.Desktop {
			log.Printf("vegad: preparação dos repositórios ignorada no perfil %s", activeProfile)
			return
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		defer stop()
		if err := dbusserver.RunFirstUpdateJobContext(ctx, activeProfile); err != nil {
			log.Fatalf("vegad first-update failed: %v", err)
		}
		return
	}

	if len(os.Args) == 4 && os.Args[1] == "backup" && os.Args[2] == "prepare-target" {
		if err := dbusserver.PrepareBackupMountTarget(os.Args[3]); err != nil {
			log.Fatalf("vegad backup prepare-target failed: %v", err)
		}
		return
	}

	if len(os.Args) == 4 && os.Args[1] == "backup" && os.Args[2] == "run" {
		configID := os.Args[3]
		log.Printf("vegad backup job %s starting", configID)
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		defer stop()
		err := dbusserver.RunScheduledBackupJob(ctx, configID, func(percent uint32, message string) {
			log.Printf("backup %s: %d%% %s", configID, percent, message)
		})
		if err != nil {
			log.Fatalf("vegad backup job %s failed: %v", configID, err)
		}
		log.Printf("vegad backup job %s finished", configID)
		return
	}

	// Every job above matched its exact arguments and returned. Anything
	// left is a typo or a stale unit: never fall through to a second daemon.
	if len(os.Args) > 1 {
		usage(os.Stderr, os.Args[1:])
		os.Exit(2)
	}

	log.Printf("vegad %s starting", version.Version)

	srv, err := dbusserver.New(activeProfile)
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

// usage explains the accepted invocations; vegad without arguments is the
// bus-activated daemon.
func usage(w io.Writer, args []string) {
	fmt.Fprintf(w, "vegad: argumentos desconhecidos: %q\n", args)
	fmt.Fprint(w, `uso:
  vegad                                   daemon D-Bus (ativado pelo barramento)
  vegad query                             consulta sem privilégios (systemd-run)
  vegad check-updates                     verificação periódica de atualizações
  vegad check-health                      verificação periódica de saúde
  vegad sync-trusted-keys                 manutenção das chaves aprovadas
  vegad first-update                      preparação dos repositórios no primeiro boot
  vegad backup prepare-target <caminho>   prepara o destino de um backup
  vegad backup run <id>                   executa um backup agendado
`)
}
