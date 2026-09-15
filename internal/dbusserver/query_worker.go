package dbusserver

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/profile"
)

func (s *Server) queryService(iface string) interface{} {
	switch iface {
	case "Metadata":
		return &MetadataService{activity: s.activity, profile: s.profile}
	case "System":
		return &SystemService{activity: s.activity}
	case "Software":
		return &SoftwareService{activity: s.activity, conn: s.conn, provider: s.provider, profile: s.profile}
	case "Monitor":
		return &MonitorService{activity: s.activity}
	case "Kernel":
		return &KernelService{activity: s.activity, conn: s.conn, provider: s.provider}
	case "Hardware":
		return &HardwareService{activity: s.activity}
	case "Users":
		return &UsersService{activity: s.activity}
	case "Services":
		return &ServicesService{activity: s.activity}
	case "Snapshots":
		return &SnapshotsService{activity: s.activity, conn: s.conn}
	case "Firewall":
		return &FirewallService{activity: s.activity}
	case "DateTime":
		return &DateTimeService{activity: s.activity}
	case "Network":
		return &NetworkService{activity: s.activity}
	case "Storage":
		return &StorageService{activity: s.activity}
	case "Bluetooth":
		if s.profile == profile.Desktop {
			return &BluetoothService{activity: s.activity}
		}
	case "Logs":
		// The root coordinator already authorized the original bus sender before
		// launching a worker with journal-group access. Running this CLI directly
		// cannot confer that group, capabilities, or root identity.
		return &LogsService{activity: s.activity, authorize: func(dbus.Sender, string) *dbus.Error { return nil }}
	}
	return nil
}

// RunQueryWorker executes a single allowlisted public query as a non-root UID.
// It is called before any administrative startup/reconciliation code in main.
func RunQueryWorker(activeProfile profile.Profile, input io.Reader, output io.Writer) error {
	if os.Geteuid() == 0 {
		return fmt.Errorf("query worker refuses root execution")
	}
	data, err := io.ReadAll(io.LimitReader(input, queryInputLimit+1))
	if err != nil {
		return err
	}
	if len(data) > queryInputLimit {
		return fmt.Errorf("query input exceeds limit")
	}
	var request queryRequest
	if err = json.Unmarshal(data, &request); err != nil {
		return err
	}
	policy, ok := methodPolicies[request.Interface+"."+request.Method]
	if !ok || (policy.privilege != queryPublic && policy.privilege != queryJournal) {
		return fmt.Errorf("operation is not an unprivileged query")
	}
	s, err := New(activeProfile)
	if err != nil {
		return err
	}
	defer s.conn.Close()
	service := s.queryService(request.Interface)
	if service == nil {
		return fmt.Errorf("query interface unavailable")
	}
	method := reflect.ValueOf(service).MethodByName(request.Method)
	if !method.IsValid() || method.Type().NumIn() != len(request.Args) {
		return fmt.Errorf("invalid query signature")
	}
	args := make([]reflect.Value, len(request.Args))
	for i, raw := range request.Args {
		target := reflect.New(method.Type().In(i))
		if err = json.Unmarshal(raw, target.Interface()); err != nil {
			return err
		}
		args[i] = target.Elem()
	}
	if monitor, ok := service.(*MonitorService); ok && request.Method == "Metrics" {
		// This worker is short-lived. Seed DRM counters before Metrics' CPU sampling
		// interval, keeping GPU sampling meaningful without a privileged cache.
		monitor.gpuPercents()
	}
	values := method.Call(args)
	reply := queryResponse{}
	last := values[len(values)-1]
	if !last.IsNil() {
		reply.Error = last.Interface().(*dbus.Error)
	} else {
		for _, v := range values[:len(values)-1] {
			encoded, err := json.Marshal(v.Interface())
			if err != nil {
				return err
			}
			reply.Values = append(reply.Values, encoded)
		}
	}
	return json.NewEncoder(output).Encode(reply)
}
