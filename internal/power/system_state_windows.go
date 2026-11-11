//go:build windows

package power

import (
	"context"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

func windowsSaverState(flag byte) (SystemState, bool) {
	if flag > 1 {
		return SystemState{}, false
	}
	return SystemState{Known: true, SaverEnabled: flag == 1}, true
}

func (nativeSystemStateSource) ReadSystemState(ctx context.Context) (SystemState, error) {
	if ctx == nil {
		return SystemState{}, ErrSystemStateUnavailable
	}
	if err := ctx.Err(); err != nil {
		return SystemState{}, err
	}
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemPowerStatus")
	if err := proc.Find(); err != nil {
		return SystemState{}, ErrSystemStateUnavailable
	}
	var status systemPowerStatus
	result, _, _ := proc.Call(uintptr(unsafe.Pointer(&status)))
	runtime.KeepAlive(&status)
	state, valid := windowsSaverState(status.SystemStatusFlag)
	if result == 0 || !valid {
		return SystemState{}, ErrSystemStateUnavailable
	}
	if err := ctx.Err(); err != nil {
		return SystemState{}, err
	}
	return state, nil
}
