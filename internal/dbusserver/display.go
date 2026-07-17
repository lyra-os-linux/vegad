package dbusserver

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

type DisplayService struct {
	activity *Activity
}

type DisplayMode struct {
	Width     uint32
	Height    uint32
	RefreshHz float64
	Current   bool
	Preferred bool
}

type DisplayOutput struct {
	Name     string
	Enabled  bool
	Primary  bool
	Scale    float64
	Rotation string
	Modes    []DisplayMode
}

func (d *DisplayService) ListOutputs() ([]DisplayOutput, *dbus.Error) {
	d.activity.Touch()
	session, err := activeGraphicalSession()
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	outputs, err := session.listOutputs()
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	return outputs, nil
}

func (d *DisplayService) Apply(output string, width, height uint32, refreshHz, scale float64, rotation string) *dbus.Error {
	d.activity.Touch()
	if output == "" {
		return dbus.MakeFailedError(fmt.Errorf("output vazio"))
	}
	session, err := activeGraphicalSession()
	if err != nil {
		return dbus.MakeFailedError(err)
	}
	if err := session.applyMode(output, width, height, refreshHz, scale, rotation); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}
