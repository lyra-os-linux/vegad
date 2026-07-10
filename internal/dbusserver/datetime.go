package dbusserver

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"
)

type DateTimeService struct {
	activity *Activity
}

type DateTimeStatus struct {
	Timezone string
	NTP      bool
	Locale   string
	Keymap   string
}

func (d *DateTimeService) Status() (DateTimeStatus, *dbus.Error) {
	d.activity.Touch()
	return DateTimeStatus{
		Timezone: currentTimezone(),
		NTP:      currentNTP(),
		Locale:   currentLocale(),
		Keymap:   currentKeymap(),
	}, nil
}

func (d *DateTimeService) ListTimezones() ([]string, *dbus.Error) {
	d.activity.Touch()
	if commandAvailable("timedatectl") {
		out, err := runCommandOutput("timedatectl", "list-timezones")
		if err == nil {
			return nonEmptyLines(out), nil
		}
	}
	return []string{"America/Sao_Paulo", "UTC", "Europe/Lisbon", "America/New_York"}, nil
}

func (d *DateTimeService) ListLocales() ([]string, *dbus.Error) {
	d.activity.Touch()
	if commandAvailable("localectl") {
		out, err := runCommandOutput("localectl", "list-locales")
		if err == nil && strings.TrimSpace(out) != "" {
			return nonEmptyLines(out), nil
		}
	}
	data, err := os.ReadFile("/etc/locale.gen")
	if err == nil {
		seen := map[string]bool{}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) > 0 && strings.Contains(fields[0], ".") {
				seen[fields[0]] = true
			}
		}
		locales := keys(seen)
		if len(locales) > 0 {
			return locales, nil
		}
	}
	return []string{"pt_BR.UTF-8", "en_US.UTF-8", "es_ES.UTF-8"}, nil
}

func (d *DateTimeService) ListKeymaps() ([]string, *dbus.Error) {
	d.activity.Touch()
	if commandAvailable("localectl") {
		out, err := runCommandOutput("localectl", "list-x11-keymap-layouts")
		if err == nil && strings.TrimSpace(out) != "" {
			return nonEmptyLines(out), nil
		}
	}
	return []string{"br", "us", "pt", "es", "de", "fr"}, nil
}

func (d *DateTimeService) Apply(sender dbus.Sender, timezone string, ntp bool, locale string, keymap string) *dbus.Error {
	d.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.system.configure"); err != nil {
		return err
	}
	if timezone != "" {
		if !commandAvailable("timedatectl") {
			return dbus.MakeFailedError(fmt.Errorf("timedatectl não está disponível"))
		}
		if err := runCommand("timedatectl", "set-timezone", timezone); err != nil {
			return dbus.MakeFailedError(err)
		}
	}
	if commandAvailable("timedatectl") {
		value := "false"
		if ntp {
			value = "true"
		}
		if err := runCommand("timedatectl", "set-ntp", value); err != nil {
			return dbus.MakeFailedError(err)
		}
	}
	if locale != "" {
		if !commandAvailable("localectl") {
			return dbus.MakeFailedError(fmt.Errorf("localectl não está disponível"))
		}
		if err := runCommand("localectl", "set-locale", "LANG="+locale); err != nil {
			return dbus.MakeFailedError(err)
		}
	}
	if keymap != "" {
		if !commandAvailable("localectl") {
			return dbus.MakeFailedError(fmt.Errorf("localectl não está disponível"))
		}
		if err := runCommand("localectl", "set-x11-keymap", keymap); err != nil {
			return dbus.MakeFailedError(err)
		}
	}
	return nil
}

func currentTimezone() string {
	if commandAvailable("timedatectl") {
		out, err := runCommandOutput("timedatectl", "show", "-p", "Timezone", "--value")
		if err == nil && out != "" {
			return out
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if idx := strings.Index(target, "zoneinfo/"); idx >= 0 {
			return target[idx+len("zoneinfo/"):]
		}
	}
	return "UTC"
}

func currentNTP() bool {
	if commandAvailable("timedatectl") {
		out, err := runCommandOutput("timedatectl", "show", "-p", "NTP", "--value")
		return err == nil && strings.TrimSpace(out) == "yes"
	}
	return false
}

func currentLocale() string {
	if commandAvailable("localectl") {
		out, err := runCommandOutput("localectl", "status")
		if err == nil {
			for _, line := range strings.Split(out, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "System Locale:") && strings.Contains(line, "LANG=") {
					return strings.TrimSpace(strings.TrimPrefix(strings.Split(line, "LANG=")[1], "\""))
				}
			}
		}
	}
	if value := os.Getenv("LANG"); value != "" {
		return value
	}
	return "pt_BR.UTF-8"
}

func currentKeymap() string {
	if commandAvailable("localectl") {
		out, err := runCommandOutput("localectl", "status")
		if err == nil {
			for _, line := range strings.Split(out, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "X11 Layout:") {
					return strings.TrimSpace(strings.TrimPrefix(line, "X11 Layout:"))
				}
			}
		}
	}
	return "br"
}

func nonEmptyLines(value string) []string {
	var rows []string
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			rows = append(rows, line)
		}
	}
	sort.Strings(rows)
	return rows
}

func keys(values map[string]bool) []string {
	rows := make([]string, 0, len(values))
	for key := range values {
		rows = append(rows, key)
	}
	sort.Strings(rows)
	return rows
}
