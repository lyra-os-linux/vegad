package dbusserver

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

type StorageService struct {
	activity *Activity
}

type StorageVolumeInfo struct {
	Name       string
	Path       string
	Type       string
	FSType     string
	Size       string
	Used       string
	Avail      string
	UsePercent uint32
	Mountpoint string
	Model      string
	Removable  bool
	CanMount   bool
	CanUnmount bool
}

type lsblkOutput struct {
	Blockdevices []lsblkDevice `json:"blockdevices"`
}

type lsblkDevice struct {
	Name       string        `json:"name"`
	Path       string        `json:"path"`
	Type       string        `json:"type"`
	FSType     string        `json:"fstype"`
	Size       string        `json:"size"`
	Mountpoint string        `json:"mountpoint"`
	Model      string        `json:"model"`
	Removable  bool          `json:"rm"`
	Children   []lsblkDevice `json:"children"`
}

func (s *StorageService) ListVolumes() ([]StorageVolumeInfo, *dbus.Error) {
	s.activity.Touch()
	if !commandAvailable("lsblk") {
		return []StorageVolumeInfo{}, nil
	}
	out, err := runCommandOutput("lsblk", "-J", "-o", "NAME,PATH,TYPE,FSTYPE,SIZE,MOUNTPOINT,MODEL,RM")
	if err != nil {
		return nil, dbus.MakeFailedError(fmt.Errorf("lsblk: %w", err))
	}
	var parsed lsblkOutput
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, dbus.MakeFailedError(fmt.Errorf("lsblk JSON inválido: %w", err))
	}
	usage := mountUsage()
	var rows []StorageVolumeInfo
	var walk func(lsblkDevice)
	walk = func(dev lsblkDevice) {
		info := StorageVolumeInfo{
			Name:       dev.Name,
			Path:       dev.Path,
			Type:       dev.Type,
			FSType:     dev.FSType,
			Size:       dev.Size,
			Mountpoint: dev.Mountpoint,
			Model:      strings.TrimSpace(dev.Model),
			Removable:  dev.Removable,
			CanMount:   dev.FSType != "" && dev.Mountpoint == "",
			CanUnmount: dev.Mountpoint != "" && dev.Mountpoint != "/" && dev.Mountpoint != "/boot",
		}
		if u, ok := usage[dev.Mountpoint]; ok {
			info.Used = u.used
			info.Avail = u.avail
			info.UsePercent = u.percent
		}
		rows = append(rows, info)
		for _, child := range dev.Children {
			walk(child)
		}
	}
	for _, dev := range parsed.Blockdevices {
		walk(dev)
	}
	return rows, nil
}

func (s *StorageService) Mount(sender dbus.Sender, path string) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.storage.mount"); err != nil {
		return err
	}
	if err := runCommand("udisksctl", "mount", "-b", path); err == nil {
		return nil
	}
	if err := runCommand("mount", path); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (s *StorageService) Unmount(sender dbus.Sender, path string) *dbus.Error {
	s.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.storage.mount"); err != nil {
		return err
	}
	if err := runCommand("udisksctl", "unmount", "-b", path); err == nil {
		return nil
	}
	if err := runCommand("umount", path); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

type dfUsage struct {
	used    string
	avail   string
	percent uint32
}

func mountUsage() map[string]dfUsage {
	rows := map[string]dfUsage{}
	if !commandAvailable("df") {
		return rows
	}
	out, err := runCommandOutput("df", "-hP")
	if err != nil {
		return rows
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[0] == "Filesystem" {
			continue
		}
		pct, _ := strconv.ParseUint(strings.TrimSuffix(fields[4], "%"), 10, 32)
		rows[fields[5]] = dfUsage{used: fields[2], avail: fields[3], percent: uint32(pct)}
	}
	return rows
}
