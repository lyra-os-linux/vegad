package dbusserver

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"
	vegai18n "github.com/lyraos/vegad/internal/i18n"
)

// ServicesService backs org.lyraos.Vega1.Services: a curated list of user-facing systemd units with enable/disable and
// start/stop controls.
type ServicesService struct {
	activity *Activity
}

type ManagedServiceInfo struct {
	Name        string
	Label       string
	Description string
	Enabled     bool
	Active      bool
	Available   bool
}

var curatedServices = []struct {
	name           string
	labelKey       string
	descriptionKey string
}{
	{name: "sshd.service", labelKey: "services.remote_access.label", descriptionKey: "services.remote_access.description"},
	{name: "bluetooth.service", labelKey: "Bluetooth", descriptionKey: "services.bluetooth.description"},
	{name: "cups.service", labelKey: "services.printing.label", descriptionKey: "services.printing.description"},
	{name: "NetworkManager.service", labelKey: "services.network.label", descriptionKey: "services.network.description"},
	{name: "firewalld.service", labelKey: "Firewall", descriptionKey: "services.firewall.description"},
	{name: "avahi-daemon.service", labelKey: "services.discovery.label", descriptionKey: "services.discovery.description"},
}

func (s *ServicesService) ListServices() ([]ManagedServiceInfo, *dbus.Error) {
	return s.ListServicesLocalized(vegai18n.DefaultLocale)
}

func (s *ServicesService) ListServicesLocalized(locale string) ([]ManagedServiceInfo, *dbus.Error) {
	s.activity.Touch()

	var rows []ManagedServiceInfo
	for _, item := range curatedServices {
		rows = append(rows, serviceInfo(item.name, vegai18n.T(locale, item.labelKey), vegai18n.T(locale, item.descriptionKey)))
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Label < rows[j].Label })
	return rows, nil
}

func (s *ServicesService) ListAllServices() ([]ManagedServiceInfo, *dbus.Error) {
	return s.ListAllServicesLocalized(vegai18n.DefaultLocale)
}

func (s *ServicesService) ListAllServicesLocalized(locale string) ([]ManagedServiceInfo, *dbus.Error) {
	s.activity.Touch()
	rows, err := listAllSystemdServices(locale)
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	return rows, nil
}

func (s *ServicesService) SetServiceEnabled(sender dbus.Sender, name string, enabled bool) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.services.configure"); err != nil {
		return err
	}
	if err := setServiceEnabled(name, enabled); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (s *ServicesService) SetServiceRunning(sender dbus.Sender, name string, running bool) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.services.configure"); err != nil {
		return err
	}
	if err := setServiceRunning(name, running); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (s *ServicesService) RestartService(sender dbus.Sender, name string) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.services.configure"); err != nil {
		return err
	}
	if err := restartService(name); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func serviceInfo(name, label, description string) ManagedServiceInfo {
	enabled := false
	active := false
	available := false

	if commandAvailable("systemctl") {
		if out, err := runCommandOutput("systemctl", "is-enabled", name); err == nil {
			enabled = strings.TrimSpace(out) == "enabled" || strings.TrimSpace(out) == "static"
		}
		if out, err := runCommandOutput("systemctl", "is-active", name); err == nil {
			active = strings.TrimSpace(out) == "active"
		}
		if out, err := runCommandOutput("systemctl", "show", "-p", "LoadState", "--value", name); err == nil {
			available = strings.TrimSpace(out) != "" && strings.TrimSpace(out) != "not-found"
		}
		if available && enabled && !active && conditionSkippedServices([]string{name})[name] {
			available = false
		}
	}

	return ManagedServiceInfo{
		Name:        name,
		Label:       label,
		Description: description,
		Enabled:     enabled,
		Active:      active,
		Available:   available,
	}
}

func listAllSystemdServices(locale string) ([]ManagedServiceInfo, error) {
	if !commandAvailable("systemctl") {
		return nil, fmt.Errorf("systemctl não está disponível")
	}

	enabledByUnit := map[string]bool{}
	availableByUnit := map[string]bool{}
	unitFiles, err := runCommandOutput("systemctl", "list-unit-files", "--type=service", "--no-legend", "--no-pager")
	if err != nil {
		return nil, fmt.Errorf("systemctl list-unit-files: %w — %s", err, unitFiles)
	}
	for _, line := range strings.Split(unitFiles, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasSuffix(fields[0], ".service") {
			continue
		}
		name := fields[0]
		state := fields[1]
		availableByUnit[name] = true
		enabledByUnit[name] = state == "enabled" || state == "enabled-runtime" || state == "static" || state == "alias"
	}

	activeByUnit := map[string]bool{}
	descriptionByUnit := map[string]string{}
	units, err := runCommandOutput("systemctl", "list-units", "--type=service", "--all", "--no-legend", "--no-pager")
	if err != nil {
		return nil, fmt.Errorf("systemctl list-units: %w — %s", err, units)
	}
	for _, line := range strings.Split(units, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "●" {
			fields = fields[1:]
		}
		if len(fields) < 4 || !strings.HasSuffix(fields[0], ".service") {
			continue
		}
		name := fields[0]
		availableByUnit[name] = true
		activeByUnit[name] = fields[2] == "active"
		if len(fields) > 4 {
			descriptionByUnit[name] = strings.Join(fields[4:], " ")
		}
	}

	var stopped []string
	for name := range availableByUnit {
		if enabledByUnit[name] && !activeByUnit[name] {
			stopped = append(stopped, name)
		}
	}
	skipped := conditionSkippedServices(stopped)

	rows := make([]ManagedServiceInfo, 0, len(availableByUnit))
	for name := range availableByUnit {
		description := descriptionByUnit[name]
		if description == "" {
			description = vegai18n.T(locale, "services.systemd.description")
		}
		rows = append(rows, ManagedServiceInfo{
			Name:        name,
			Label:       serviceLabelFromName(name),
			Description: description,
			Enabled:     enabledByUnit[name],
			Active:      activeByUnit[name],
			Available:   !skipped[name],
		})
	}

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

// conditionSkippedServices reports the inactive units that cannot run on this
// machine because a Condition*= is unmet (e.g. bluetooth.service without an
// adapter). They are enabled but are not failures, so they are reported as
// unavailable instead. When systemd already checked the conditions, its
// ConditionResult decides. A zero ConditionTimestampMonotonic means the unit
// was never started (bluetooth.service is only pulled in by bluetooth.target
// when an adapter appears), so the conditions are read over D-Bus and the
// path-based ones evaluated here.
func conditionSkippedServices(names []string) map[string]bool {
	skipped := map[string]bool{}
	// Templates (getty@.service) are never active, and systemctl show
	// aborts the whole call on them.
	var units []string
	for _, name := range names {
		if !strings.HasSuffix(name, "@.service") {
			units = append(units, name)
		}
	}
	if len(units) == 0 {
		return skipped
	}
	args := append([]string{"show", "-p", "Id", "-p", "ActiveState", "-p", "ConditionResult", "-p", "ConditionTimestampMonotonic", "--"}, units...)
	out, err := runCommandOutput("systemctl", args...)
	if err != nil {
		return skipped
	}
	skipped, unchecked := parseConditionSkipped(out, units)
	if len(unchecked) == 0 {
		return skipped
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return skipped
	}
	defer conn.Close()
	for _, name := range unchecked {
		if conditions, err := unitConditions(conn, name); err == nil && !conditionsMet(conditions) {
			skipped[name] = true
		}
	}
	return skipped
}

// parseConditionSkipped returns the inactive units whose last condition check
// failed, and the inactive units whose conditions were never checked. systemctl
// show prints one block per requested name, in order; the results are keyed by
// the requested name, because an alias (dbus-org.bluez.service) reports the
// Id of its target.
func parseConditionSkipped(out string, names []string) (map[string]bool, []string) {
	skipped := map[string]bool{}
	var unchecked []string
	blocks := strings.Split(out, "\n\n")
	for i, block := range blocks {
		props := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
				props[key] = value
			}
		}
		name := props["Id"]
		if len(blocks) == len(names) {
			name = names[i]
		}
		if name == "" || props["ActiveState"] != "inactive" {
			continue
		}
		if ts := props["ConditionTimestampMonotonic"]; ts == "" || ts == "0" {
			unchecked = append(unchecked, name)
			continue
		}
		if props["ConditionResult"] == "no" {
			skipped[name] = true
		}
	}
	return skipped, unchecked
}

// unitCondition mirrors an entry of the systemd Unit.Conditions property,
// a(sbbsi). State is positive when the condition passed, negative when it
// failed and zero when it was never checked.
type unitCondition struct {
	Type    string
	Trigger bool
	Negate  bool
	Param   string
	State   int32
}

func unitConditions(conn *dbus.Conn, name string) ([]unitCondition, error) {
	obj := conn.Object("org.freedesktop.systemd1", systemdUnitPath(name))
	value, err := obj.GetProperty("org.freedesktop.systemd1.Unit.Conditions")
	if err != nil {
		return nil, err
	}
	var conditions []unitCondition
	if err := value.Store(&conditions); err != nil {
		return nil, err
	}
	return conditions, nil
}

// systemdUnitPath escapes a unit name like systemd's bus_label_escape.
func systemdUnitPath(name string) dbus.ObjectPath {
	var b strings.Builder
	b.WriteString("/org/freedesktop/systemd1/unit/")
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (i > 0 && c >= '0' && c <= '9') {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "_%02x", c)
	}
	return dbus.ObjectPath(b.String())
}

// conditionsMet follows systemd's rule: every plain condition must pass and,
// when there are triggering ("|") conditions, at least one of them. A
// condition this code cannot evaluate counts as passed, so a unit is never
// hidden on a guess.
func conditionsMet(conditions []unitCondition) bool {
	hasTrigger, triggerMet := false, false
	for _, condition := range conditions {
		met, known := evaluateCondition(condition)
		if !known {
			met = true
		}
		if condition.Trigger {
			hasTrigger = true
			triggerMet = triggerMet || met
			continue
		}
		if !met {
			return false
		}
	}
	return !hasTrigger || triggerMet
}

func evaluateCondition(condition unitCondition) (met bool, known bool) {
	if condition.State != 0 {
		return condition.State > 0, true
	}
	var ok bool
	switch condition.Type {
	case "ConditionPathExists":
		_, err := os.Stat(condition.Param)
		ok = err == nil
	case "ConditionPathIsDirectory":
		info, err := os.Stat(condition.Param)
		ok = err == nil && info.IsDir()
	case "ConditionDirectoryNotEmpty":
		entries, err := os.ReadDir(condition.Param)
		ok = err == nil && len(entries) > 0
	default:
		return false, false
	}
	return ok != condition.Negate, true
}

func serviceLabelFromName(name string) string {
	label := strings.TrimSuffix(name, ".service")
	label = strings.ReplaceAll(label, "-", " ")
	label = strings.ReplaceAll(label, "_", " ")
	if label == "" {
		return name
	}
	return label
}

func setServiceEnabled(name string, enabled bool) error {
	if !commandAvailable("systemctl") {
		return fmt.Errorf("systemctl não está disponível")
	}

	args := []string{}
	if enabled {
		args = append(args, "enable", "--now", name)
	} else {
		args = append(args, "disable", "--now", name)
	}
	if out, err := runCommandOutput("systemctl", args...); err != nil {
		return fmt.Errorf("systemctl %s: %w — %s", strings.Join(args, " "), err, out)
	}
	return nil
}

func setServiceRunning(name string, running bool) error {
	if !commandAvailable("systemctl") {
		return fmt.Errorf("systemctl não está disponível")
	}

	action := "stop"
	if running {
		action = "start"
	}
	if out, err := runCommandOutput("systemctl", action, name); err != nil {
		return fmt.Errorf("systemctl %s %s: %w — %s", action, name, err, out)
	}
	return nil
}

func restartService(name string) error {
	if !commandAvailable("systemctl") {
		return fmt.Errorf("systemctl não está disponível")
	}
	if out, err := runCommandOutput("systemctl", "restart", name); err != nil {
		return fmt.Errorf("systemctl restart %s: %w — %s", name, err, out)
	}
	return nil
}
