package dbusserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/profile"
)

const queryInputLimit = 64 * 1024
const queryOutputLimit = 16 * 1024 * 1024

var querySlots = make(chan struct{}, 4)
var senderType = reflect.TypeOf(dbus.Sender(""))

type queryRequest struct {
	Interface string
	Method    string
	Args      []json.RawMessage
}
type queryResponse struct {
	Values []json.RawMessage
	Error  *dbus.Error
}

func methodFailure(t reflect.Type, err *dbus.Error) []reflect.Value {
	result := make([]reflect.Value, t.NumOut())
	for i := range result {
		result[i] = reflect.Zero(t.Out(i))
	}
	result[len(result)-1] = reflect.ValueOf(err)
	return result
}

// Routing is explicit and verified before the public bus name is acquired.
// Hidden dbus.Sender parameters do not change the public wire signature.
func (s *Server) routedMethods(service interface{}, iface string) (map[string]interface{}, error) {
	methods := trackedMethods(service, s.activity)
	if iface == "org.freedesktop.DBus.Introspectable" {
		return methods, nil
	}
	short := strings.TrimPrefix(iface, BusName+".")
	for name, original := range methods {
		policy, known := methodPolicies[short+"."+name]
		if !known {
			return nil, fmt.Errorf("unclassified D-Bus method %s.%s", short, name)
		}
		if policy.privilege == administrative || policy.privilege == maintenance || policy.privilege == disabledOperation {
			continue
		}
		fn := reflect.ValueOf(original)
		originalType := fn.Type()
		senderIndex := -1
		inputs := make([]reflect.Type, originalType.NumIn())
		outputs := make([]reflect.Type, originalType.NumOut())
		for i := range inputs {
			inputs[i] = originalType.In(i)
			if inputs[i] == senderType {
				senderIndex = i
			}
		}
		for i := range outputs {
			outputs[i] = originalType.Out(i)
		}
		addedSender := senderIndex < 0
		if addedSender {
			inputs = append([]reflect.Type{senderType}, inputs...)
			senderIndex = 0
		}
		routedType := reflect.FuncOf(inputs, outputs, false)
		methods[name] = reflect.MakeFunc(routedType, func(args []reflect.Value) []reflect.Value {
			done, ok := s.activity.begin()
			if !ok {
				return methodFailure(routedType, dbus.NewError(BusName+".Error.ShuttingDown", []interface{}{"daemon shutting down"}))
			}
			defer done()
			sender := args[senderIndex].Interface().(dbus.Sender)
			originalArgs := args
			if addedSender {
				originalArgs = args[1:]
			}
			if policy.action != "" {
				authorize := requirePolkit
				if policy.privilege == privilegedRead {
					authorize = requirePolkitRead
				}
				if err := authorize(sender, policy.action); err != nil {
					return methodFailure(routedType, err)
				}
			}
			if policy.privilege == privilegedRead || os.Geteuid() != 0 {
				return fn.Call(originalArgs)
			}
			caller, err := resolveDesktopUser(s.conn, sender)
			if err != nil {
				return methodFailure(routedType, dbus.MakeFailedError(err))
			}
			request := queryRequest{Interface: short, Method: name}
			for _, arg := range originalArgs {
				encoded, err := json.Marshal(arg.Interface())
				if err != nil {
					return methodFailure(routedType, dbus.MakeFailedError(err))
				}
				request.Args = append(request.Args, encoded)
			}
			reply, err := s.query(request, caller, policy.privilege == queryJournal)
			if err != nil {
				return methodFailure(routedType, dbus.MakeFailedError(err))
			}
			if reply.Error != nil {
				return methodFailure(routedType, reply.Error)
			}
			if len(reply.Values) != len(outputs)-1 {
				return methodFailure(routedType, dbus.MakeFailedError(fmt.Errorf("invalid query result count")))
			}
			values := make([]reflect.Value, len(outputs))
			for i, raw := range reply.Values {
				target := reflect.New(outputs[i])
				if err := json.Unmarshal(raw, target.Interface()); err != nil {
					return methodFailure(routedType, dbus.MakeFailedError(err))
				}
				values[i] = target.Elem()
			}
			values[len(values)-1] = reflect.Zero(outputs[len(outputs)-1])
			return values
		}).Interface()
	}
	return methods, nil
}

func queryProperties(caller *desktopUser, journal bool) ([]string, error) {
	properties := []string{
		"NoNewPrivileges=yes", "CapabilityBoundingSet=", "AmbientCapabilities=",
		"ProtectSystem=strict", "ProtectHome=read-only", "PrivateTmp=yes",
		"ProtectControlGroups=yes", "ProtectKernelTunables=yes", "ProtectKernelModules=yes",
		"ProtectKernelLogs=yes", "ProtectClock=yes", "ProtectHostname=yes",
		"RestrictRealtime=yes", "RestrictSUIDSGID=yes", "LockPersonality=yes",
		"RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK", "SystemCallArchitectures=native",
		"UMask=0077", "RuntimeMaxSec=60", "TimeoutStartSec=8", "TimeoutStopSec=1",
		"TasksMax=32", "MemoryMax=256M", "KillMode=control-group",
	}
	if caller == nil {
		properties = append(properties, "User=vegad-query", "Group=vegad-query", "ProtectHome=yes", "PrivateDevices=yes")
	} else {
		if caller.Uid == 0 || !filepath.IsAbs(caller.HomeDir) || filepath.Clean(caller.HomeDir) != caller.HomeDir || strings.ContainsAny(caller.HomeDir, "\x00\r\n") {
			return nil, fmt.Errorf("invalid query caller identity")
		}
		properties = append(properties, "User="+strconv.FormatUint(uint64(caller.Uid), 10), "Group="+strconv.FormatUint(uint64(caller.Gid), 10))
		// Flatpak metadata may update the caller's caches. All system paths remain
		// read-only; no cache refresh can turn into a privileged package operation.
		for _, path := range []string{filepath.Join(caller.HomeDir, ".cache"), filepath.Join(caller.HomeDir, ".local/share/flatpak")} {
			properties = append(properties, "ReadWritePaths=-"+strconv.Quote(strings.ReplaceAll(path, "%", "%%")))
		}
	}
	if journal {
		properties = append(properties, "SupplementaryGroups=systemd-journal")
	}
	return properties, nil
}

func queryCommand(ctx context.Context, caller *desktopUser, journal bool, activeProfile profile.Profile, requests ...queryRequest) (*exec.Cmd, error) {
	properties, err := queryProperties(caller, journal)
	if err != nil {
		return nil, err
	}
	if _, err := profile.Parse(string(activeProfile)); err != nil {
		return nil, err
	}
	args := []string{"--setenv=VEGAD_PROFILE=" + string(activeProfile), "--quiet", "--wait", "--pipe", "--collect", "--service-type=exec", "--expand-environment=no",
		"--setenv=LC_ALL=C", "--setenv=GOMAXPROCS=2", "--setenv=PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	// A private unit name lets the coordinator stop the complete worker cgroup
	// if the launcher loses its bus connection or reaches its own deadline.
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	args = append(args, fmt.Sprintf("--unit=vegad-query-%x.service", nonce))
	if path := os.Getenv("VEGAD_UPDATE_STATE"); path != "" {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
			return nil, fmt.Errorf("invalid update state path")
		}
		args = append(args, "--setenv=VEGAD_UPDATE_STATE="+path)
	}
	if caller != nil {
		args = append(args, "--setenv=HOME="+caller.HomeDir, "--setenv=XDG_RUNTIME_DIR="+caller.RuntimeDir)
	} else {
		// The service account uses only the unit's private temporary cache.
		args = append(args, "--setenv=HOME=/tmp", "--setenv=XDG_CACHE_HOME=/tmp/vega-query-cache")
	}
	if len(requests) == 1 && requests[0].Interface == "Software" && (requests[0].Method == "NvidiaStatus" || requests[0].Method == "CheckNvidia") {
		// ProtectKernelModules also hides /usr/lib/modules, including the
		// target of /boot/vmlinuz. These two public diagnostics need file
		// reads, not module administration. Retain the read-only filesystem,
		// caller UID, empty capabilities and NoNewPrivileges; explicitly deny
		// module syscalls even though their files are now visible.
		for i, p := range properties {
			if p == "ProtectKernelModules=yes" {
				properties[i] = "ProtectKernelModules=no"
			}
		}
		properties = append(properties, "SystemCallFilter=~@module")
	}
	for _, property := range properties {
		args = append(args, "--property="+property)
	}
	args = append(args, "--", "/usr/lib/vega/vegad", "query")
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.WaitDelay = 2 * time.Second
	return cmd, nil
}

func stopQueryUnit(cmd *exec.Cmd) {
	for _, arg := range cmd.Args {
		if !strings.HasPrefix(arg, "--unit=vegad-query-") {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		stop := exec.CommandContext(ctx, "/usr/bin/systemctl", "stop", strings.TrimPrefix(arg, "--unit="))
		stop.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
		_ = stop.Run() // The unit may not have been created; RuntimeMaxSec is the fallback.
		return
	}
}

type boundedOutput struct {
	data  []byte
	limit int
	full  bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	remaining := b.limit - len(b.data)
	if len(p) > remaining {
		b.full = true
		b.data = append(b.data, p[:remaining]...)
	} else {
		b.data = append(b.data, p...)
	}
	return len(p), nil
}

func executeQuery(request queryRequest, caller *desktopUser, journal bool, activeProfile profile.Profile) (queryResponse, error) {
	var reply queryResponse
	body, err := json.Marshal(request)
	if err != nil {
		return reply, err
	}
	if len(body) > queryInputLimit {
		return reply, fmt.Errorf("query input exceeds limit")
	}
	queueContext, queueCancel := context.WithTimeout(context.Background(), 64*time.Second)
	defer queueCancel()
	select {
	case querySlots <- struct{}{}:
		defer func() { <-querySlots }()
	case <-queueContext.Done():
		return reply, queueContext.Err()
	}
	queueCancel()
	// Queueing must not consume the worker's runtime budget. Keep the launch
	// slot until the service has exited (8s start + 60s runtime + 1s stop).
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	cmd, err := queryCommand(ctx, caller, journal, activeProfile, request)
	if err != nil {
		return reply, err
	}
	stdout := &boundedOutput{limit: queryOutputLimit}
	stderr := &boundedOutput{limit: 32 * 1024}
	cmd.Stdin = bytes.NewReader(body)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	if err != nil {
		stopQueryUnit(cmd)
	}
	if ctx.Err() != nil {
		return reply, ctx.Err()
	}
	if err != nil {
		return reply, fmt.Errorf("unprivileged query: %w: %s", err, strings.TrimSpace(string(stderr.data)))
	}
	if stdout.full {
		return reply, fmt.Errorf("query response exceeded limit")
	}
	dec := json.NewDecoder(bytes.NewReader(stdout.data))
	if err = dec.Decode(&reply); err != nil {
		return reply, err
	}
	var extra interface{}
	if err = dec.Decode(&extra); err != io.EOF {
		return reply, fmt.Errorf("extra query response data")
	}
	return reply, nil
}
