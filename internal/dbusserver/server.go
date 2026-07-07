// Package dbusserver wires vegad onto the D-Bus system bus and exports the
// org.lyraos.Vega1.* interfaces described in PROMPT-VEGA.md §2.2.
//
// vegad is bus-activated (systemd Type=dbus): it does not run idle
// permanently. Every exported method call touches the shared Activity
// tracker; once IdleTimeout elapses without activity the daemon releases
// the bus name and exits, letting systemd re-activate it on demand.
package dbusserver

import (
	"log"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

const (
	BusName    = "org.lyraos.Vega1"
	ObjectPath = dbus.ObjectPath("/org/lyraos/Vega1")

	// IdleTimeout is how long vegad waits without any D-Bus activity
	// before releasing the bus name and exiting.
	IdleTimeout = 2 * time.Minute
)

// Activity tracks the last time any exported method was invoked, so the
// server can decide when it's safe to exit under bus activation.
type Activity struct {
	mu   sync.Mutex
	last time.Time
}

func (a *Activity) Touch() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.last = time.Now()
}

func (a *Activity) idleFor() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return time.Since(a.last)
}

// Server owns the system bus connection and the lifecycle of the exported
// interfaces.
type Server struct {
	conn     *dbus.Conn
	activity *Activity
}

func New() (*Server, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	return &Server{conn: conn, activity: &Activity{last: time.Now()}}, nil
}

// Export registers every subsystem interface at ObjectPath and requests
// BusName. Call Run afterwards to block until idle shutdown.
func (s *Server) Export() error {
	system := &SystemService{activity: s.activity}
	if err := s.conn.Export(system, ObjectPath, BusName+".System"); err != nil {
		return err
	}

	software := &SoftwareService{activity: s.activity, conn: s.conn}
	if err := s.conn.Export(software, ObjectPath, BusName+".Software"); err != nil {
		return err
	}

	snapshots := &SnapshotsService{activity: s.activity, conn: s.conn}
	if err := s.conn.Export(snapshots, ObjectPath, BusName+".Snapshots"); err != nil {
		return err
	}

	backup := &BackupService{activity: s.activity, conn: s.conn}
	if err := s.conn.Export(backup, ObjectPath, BusName+".Backup"); err != nil {
		return err
	}

	hardware := &HardwareService{activity: s.activity}
	if err := s.conn.Export(hardware, ObjectPath, BusName+".Hardware"); err != nil {
		return err
	}

	kernel := &KernelService{activity: s.activity}
	if err := s.conn.Export(kernel, ObjectPath, BusName+".Kernel"); err != nil {
		return err
	}

	users := &UsersService{activity: s.activity}
	if err := s.conn.Export(users, ObjectPath, BusName+".Users"); err != nil {
		return err
	}

	firewall := &FirewallService{activity: s.activity}
	if err := s.conn.Export(firewall, ObjectPath, BusName+".Firewall"); err != nil {
		return err
	}

	services := &ServicesService{activity: s.activity}
	if err := s.conn.Export(services, ObjectPath, BusName+".Services"); err != nil {
		return err
	}

	// dbus-next (and any well-behaved D-Bus client) calls Introspect() to
	// discover method signatures before invoking them — godbus doesn't
	// provide this automatically, so without it every call from such a
	// client fails with "does not implement Introspectable" even though
	// gdbus/busctl (which can be told the interface up front) work fine.
	node := &introspect.Node{
		Name: string(ObjectPath),
		Interfaces: []introspect.Interface{
			{Name: BusName + ".System", Methods: introspect.Methods(system)},
			{Name: BusName + ".Software", Methods: introspect.Methods(software), Signals: []introspect.Signal{
				{Name: "TransactionProgress", Args: []introspect.Arg{
					{Name: "transactionId", Type: "u", Direction: "out"},
					{Name: "percent", Type: "u", Direction: "out"},
					{Name: "message", Type: "s", Direction: "out"},
				}},
				{Name: "TransactionFinished", Args: []introspect.Arg{
					{Name: "transactionId", Type: "u", Direction: "out"},
					{Name: "success", Type: "b", Direction: "out"},
					{Name: "message", Type: "s", Direction: "out"},
				}},
			}},
			{Name: BusName + ".Snapshots", Methods: introspect.Methods(snapshots)},
			{Name: BusName + ".Backup", Methods: introspect.Methods(backup), Signals: []introspect.Signal{
				{Name: "BackupProgress", Args: []introspect.Arg{
					{Name: "transactionId", Type: "u", Direction: "out"},
					{Name: "percent", Type: "u", Direction: "out"},
					{Name: "message", Type: "s", Direction: "out"},
				}},
				{Name: "BackupFinished", Args: []introspect.Arg{
					{Name: "transactionId", Type: "u", Direction: "out"},
					{Name: "success", Type: "b", Direction: "out"},
					{Name: "message", Type: "s", Direction: "out"},
				}},
				{Name: "RestoreProgress", Args: []introspect.Arg{
					{Name: "transactionId", Type: "u", Direction: "out"},
					{Name: "percent", Type: "u", Direction: "out"},
					{Name: "message", Type: "s", Direction: "out"},
				}},
				{Name: "RestoreFinished", Args: []introspect.Arg{
					{Name: "transactionId", Type: "u", Direction: "out"},
					{Name: "success", Type: "b", Direction: "out"},
					{Name: "message", Type: "s", Direction: "out"},
				}},
			}},
			{Name: BusName + ".Hardware", Methods: introspect.Methods(hardware)},
			{Name: BusName + ".Kernel", Methods: introspect.Methods(kernel)},
			{Name: BusName + ".Users", Methods: introspect.Methods(users)},
			{Name: BusName + ".Firewall", Methods: introspect.Methods(firewall)},
			{Name: BusName + ".Services", Methods: introspect.Methods(services)},
		},
	}
	if err := s.conn.Export(introspect.NewIntrospectable(node), ObjectPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		return err
	}

	reply, err := s.conn.RequestName(BusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		log.Printf("vegad: bus name %s already owned elsewhere", BusName)
	}
	return nil
}

// Run blocks until the daemon has been idle for longer than IdleTimeout,
// then releases the bus name so systemd can tear the process down.
func (s *Server) Run() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if s.activity.idleFor() >= IdleTimeout {
			log.Printf("vegad: idle for %s, releasing %s", IdleTimeout, BusName)
			return
		}
	}
}

func (s *Server) Close() {
	_, _ = s.conn.ReleaseName(BusName)
	_ = s.conn.Close()
}
