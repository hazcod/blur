//go:build linux

package power

import (
	"context"

	"github.com/godbus/dbus/v5"
)

const (
	powerProfilesService   = linuxPowerProfilesService
	powerProfilesPath      = dbus.ObjectPath(linuxPowerProfilesPath)
	powerProfilesInterface = linuxPowerProfilesInterface
)

func linuxProfileState(profile string) SystemState {
	switch profile {
	case "power-saver":
		return SystemState{Known: true, SaverEnabled: true}
	case "balanced", "performance":
		return SystemState{Known: true}
	default:
		return SystemState{}
	}
}

func (nativeSystemStateSource) ReadSystemState(ctx context.Context) (SystemState, error) {
	if ctx == nil {
		return SystemState{}, ErrSystemStateUnavailable
	}
	if err := ctx.Err(); err != nil {
		return SystemState{}, err
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return SystemState{}, ErrSystemStateUnavailable
	}
	defer conn.Close()
	var profile dbus.Variant
	err = conn.Object(powerProfilesService, powerProfilesPath).
		CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, powerProfilesInterface, "ActiveProfile").Store(&profile)
	if err != nil {
		return SystemState{}, ErrSystemStateUnavailable
	}
	active, ok := profile.Value().(string)
	if !ok {
		return SystemState{}, ErrSystemStateUnavailable
	}
	state := linuxProfileState(active)
	if !state.Known {
		return SystemState{}, ErrSystemStateUnavailable
	}
	if err := ctx.Err(); err != nil {
		return SystemState{}, err
	}
	return state, nil
}
