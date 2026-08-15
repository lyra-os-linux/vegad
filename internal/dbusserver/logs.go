package dbusserver

import (
	"bufio"
	"fmt"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"
)

// LogsService backs org.lyraos.Vega1.Logs (issue #14): a read-only
// journalctl viewer. vegad executes journalctl as root, so every method uses
// a dedicated administrative Polkit action.
type LogsService struct {
	activity  *Activity
	authorize func(dbus.Sender, string) *dbus.Error
	run       func(string, ...string) (string, error)
}

func (s *LogsService) authorizeRead(sender dbus.Sender) *dbus.Error {
	if s.authorize != nil {
		return s.authorize(sender, "org.lyraos.vega.logs.read-admin")
	}
	return requirePolkit(sender, "org.lyraos.vega.logs.read-admin")
}

func (s *LogsService) runCommand(name string, args ...string) (string, error) {
	if s.run != nil {
		return s.run(name, args...)
	}
	return runCommandOutput(name, args...)
}

// Query returns up to maxLines formatted journal lines (most recent last),
// filtered by unit/priority/since/search — any of which may be "" to mean
// "no filter" for that field. priority follows journalctl's own vocabulary
// (emerg, alert, crit, err, warning, notice, info, debug) and means "at
// this severity or worse", same as journalctl -p. since is a journalctl
// --since value (e.g. "-15min", "-1hour", "-24hour", "-7day").
func (s *LogsService) Query(sender dbus.Sender, unit, priority, since, search string, maxLines uint32) ([]string, *dbus.Error) {
	s.activity.Touch()
	if err := s.authorizeRead(sender); err != nil {
		return nil, err
	}

	if maxLines == 0 || maxLines > 2000 {
		maxLines = 500
	}

	args := []string{"--no-pager", "-o", "short-iso", "-n", fmt.Sprint(maxLines)}
	if unit != "" {
		args = append(args, "-u", unit)
	}
	if priority != "" {
		args = append(args, "-p", priority)
	}
	if since != "" {
		args = append(args, "--since", since)
	}
	if search != "" {
		args = append(args, "--grep", search)
	}

	out, err := s.runCommand("journalctl", args...)
	if err != nil {
		return nil, dbus.MakeFailedError(fmt.Errorf("journalctl: %w — %s", err, out))
	}
	if out == "" || out == "-- No entries --" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// ListUnits returns every systemd unit name the journal has entries for,
// sorted, for the UI's unit filter.
func (s *LogsService) ListUnits(sender dbus.Sender) ([]string, *dbus.Error) {
	s.activity.Touch()
	if err := s.authorizeRead(sender); err != nil {
		return nil, err
	}

	out, err := s.runCommand("journalctl", "--no-pager", "--field=_SYSTEMD_UNIT")
	if err != nil {
		return nil, dbus.MakeFailedError(fmt.Errorf("journalctl --field=_SYSTEMD_UNIT: %w — %s", err, out))
	}

	var units []string
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			units = append(units, line)
		}
	}
	sort.Strings(units)
	return units, nil
}
