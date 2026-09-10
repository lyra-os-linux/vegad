// Package dbusserver wires vegad onto the D-Bus system bus and exports the
// org.lyraos.Vega1.* interfaces.
//
// vegad is bus-activated (systemd Type=dbus): it does not run idle
// permanently. Every exported method call touches the shared Activity
// tracker; once IdleTimeout elapses without activity the daemon releases
// the bus name and exits, letting systemd re-activate it on demand.
package dbusserver

import (
	"fmt"
	"log"
	"reflect"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/lyraos/vegad/internal/distro"
	"github.com/lyraos/vegad/internal/profile"
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
	mu       sync.Mutex
	last     time.Time
	active   int
	stopping bool
}

func (a *Activity) Touch() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.last = time.Now()
}

func (a *Activity) idleFor() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active != 0 {
		return 0
	}
	return time.Since(a.last)
}

// begin and stopIfIdle share the same lock: once idle shutdown is committed,
// queued D-Bus calls cannot start work on the retiring daemon.
func (a *Activity) begin() (func(), bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopping {
		return nil, false
	}
	a.active++
	a.last = time.Now()
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.active--
		a.last = time.Now()
	}, true
}

func (a *Activity) stopIfIdle(timeout time.Duration) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active != 0 || time.Since(a.last) < timeout {
		return false
	}
	a.stopping = true
	return true
}

func trackedMethods(service interface{}, activity *Activity) map[string]interface{} {
	value := reflect.ValueOf(service)
	methods := make(map[string]interface{})
	errorType := reflect.TypeOf((*dbus.Error)(nil))
	for i := 0; i < value.NumMethod(); i++ {
		method := value.Method(i)
		typeOf := method.Type()
		if typeOf.NumOut() == 0 || typeOf.Out(typeOf.NumOut()-1) != errorType {
			continue
		}
		methods[value.Type().Method(i).Name] = reflect.MakeFunc(typeOf, func(args []reflect.Value) []reflect.Value {
			done, ok := activity.begin()
			if !ok {
				result := make([]reflect.Value, typeOf.NumOut())
				for i := range result {
					result[i] = reflect.Zero(typeOf.Out(i))
				}
				result[len(result)-1] = reflect.ValueOf(dbus.NewError(BusName+".Error.ShuttingDown", []interface{}{"daemon encerrando; repita a chamada"}))
				return result
			}
			defer done()
			return method.Call(args)
		}).Interface()
	}
	return methods
}

func (s *Server) export(service interface{}, iface string) error {
	return s.conn.ExportMethodTable(trackedMethods(service, s.activity), ObjectPath, iface)
}

// Server owns the system bus connection and the lifecycle of the exported
// interfaces.
type Server struct {
	conn     *dbus.Conn
	activity *Activity
	provider distro.Provider
	profile  profile.Profile
}

func New(activeProfile profile.Profile) (*Server, error) {
	id, err := distro.Detect()
	if err != nil {
		return nil, fmt.Errorf("vegad: %w", err)
	}
	provider, err := distro.NewProvider(id)
	if err != nil {
		return nil, fmt.Errorf("vegad: %w", err)
	}

	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	return &Server{conn: conn, activity: &Activity{last: time.Now()}, provider: provider, profile: activeProfile}, nil
}

// Export registers every subsystem interface at ObjectPath and requests
// BusName. Call Run afterwards to block until idle shutdown.
func (s *Server) Export() error {
	metadata := &MetadataService{activity: s.activity, profile: s.profile}
	if err := s.export(metadata, BusName+".Metadata"); err != nil {
		return err
	}

	system := &SystemService{activity: s.activity}
	if err := s.export(system, BusName+".System"); err != nil {
		return err
	}

	software := &SoftwareService{activity: s.activity, conn: s.conn, provider: s.provider, profile: s.profile}
	if err := s.export(software, BusName+".Software"); err != nil {
		return err
	}

	snapshots := &SnapshotsService{activity: s.activity, conn: s.conn}
	if err := s.export(snapshots, BusName+".Snapshots"); err != nil {
		return err
	}

	backup := &BackupService{activity: s.activity, conn: s.conn}
	if err := s.export(backup, BusName+".Backup"); err != nil {
		return err
	}

	hardware := &HardwareService{activity: s.activity}
	if err := s.export(hardware, BusName+".Hardware"); err != nil {
		return err
	}

	kernel := &KernelService{activity: s.activity, conn: s.conn, provider: s.provider}
	if err := s.export(kernel, BusName+".Kernel"); err != nil {
		return err
	}

	users := &UsersService{activity: s.activity}
	if err := s.export(users, BusName+".Users"); err != nil {
		return err
	}

	firewall := &FirewallService{activity: s.activity}
	if err := s.export(firewall, BusName+".Firewall"); err != nil {
		return err
	}

	services := &ServicesService{activity: s.activity}
	if err := s.export(services, BusName+".Services"); err != nil {
		return err
	}

	logs := &LogsService{activity: s.activity}
	if err := s.export(logs, BusName+".Logs"); err != nil {
		return err
	}

	dateTime := &DateTimeService{activity: s.activity}
	if err := s.export(dateTime, BusName+".DateTime"); err != nil {
		return err
	}

	network := &NetworkService{activity: s.activity}
	if err := s.export(network, BusName+".Network"); err != nil {
		return err
	}

	var bluetooth *BluetoothService
	if s.profile == profile.Desktop {
		bluetooth = &BluetoothService{activity: s.activity}
		if err := s.export(bluetooth, BusName+".Bluetooth"); err != nil {
			return err
		}
	}

	storage := &StorageService{activity: s.activity}
	if err := s.export(storage, BusName+".Storage"); err != nil {
		return err
	}

	monitor := &MonitorService{activity: s.activity}
	if err := s.export(monitor, BusName+".Monitor"); err != nil {
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
			{Name: BusName + ".Metadata", Methods: introspect.Methods(metadata)},
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
				{Name: "UpdatesAvailable", Args: []introspect.Arg{
					{Name: "count", Type: "u", Direction: "out"},
				}},
				{Name: "UpdateStateChanged", Args: []introspect.Arg{
					{Name: "status", Type: "(ssuuuubs)", Direction: "out"},
				}},
				{Name: "RepoKeyPending", Args: []introspect.Arg{
					{Name: "transactionId", Type: "u", Direction: "out"},
					{Name: "repo", Type: "s", Direction: "out"},
					{Name: "keyId", Type: "s", Direction: "out"},
					{Name: "fingerprint", Type: "s", Direction: "out"},
					{Name: "userId", Type: "s", Direction: "out"},
				}},
				{Name: "PackageProgress", Args: []introspect.Arg{
					{Name: "transactionId", Type: "u", Direction: "out"},
					{Name: "package", Type: "s", Direction: "out"},
					{Name: "phase", Type: "s", Direction: "out"},
					{Name: "percent", Type: "u", Direction: "out"},
				}},
				{Name: "TransactionConsoleLine", Args: []introspect.Arg{
					{Name: "transaction_id", Type: "u", Direction: "out"},
					{Name: "source", Type: "s", Direction: "out"},
					{Name: "line", Type: "s", Direction: "out"},
				}},
			}},
			{Name: BusName + ".Snapshots", Methods: introspect.Methods(snapshots)},
			{Name: BusName + ".Logs", Methods: introspect.Methods(logs)},
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
				{Name: "BackupAlert", Args: []introspect.Arg{
					{Name: "configId", Type: "s", Direction: "out"},
					{Name: "consecutiveFailures", Type: "u", Direction: "out"},
					{Name: "message", Type: "s", Direction: "out"},
				}},
			}},
			{Name: BusName + ".Hardware", Methods: introspect.Methods(hardware)},
			{Name: BusName + ".Kernel", Methods: introspect.Methods(kernel)},
			{Name: BusName + ".Users", Methods: introspect.Methods(users)},
			{Name: BusName + ".Firewall", Methods: introspect.Methods(firewall)},
			{Name: BusName + ".Services", Methods: introspect.Methods(services)},
			{Name: BusName + ".DateTime", Methods: introspect.Methods(dateTime)},
			{Name: BusName + ".Network", Methods: introspect.Methods(network)},
			{Name: BusName + ".Storage", Methods: introspect.Methods(storage)},
			{Name: BusName + ".Monitor", Methods: introspect.Methods(monitor)},
		},
	}
	if bluetooth != nil {
		node.Interfaces = append(node.Interfaces, introspect.Interface{Name: BusName + ".Bluetooth", Methods: introspect.Methods(bluetooth)})
	}
	if err := s.export(introspect.NewIntrospectable(node), "org.freedesktop.DBus.Introspectable"); err != nil {
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
		if s.activity.stopIfIdle(IdleTimeout) {
			log.Printf("vegad: idle for %s, releasing %s", IdleTimeout, BusName)
			return
		}
	}
}

func (s *Server) Close() {
	_, _ = s.conn.ReleaseName(BusName)
	_ = s.conn.Close()
}
