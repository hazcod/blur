//go:build darwin

package power

import (
	"context"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

var foundationOnce sync.Once
var foundationErr error

func (nativeSystemStateSource) ReadSystemState(ctx context.Context) (SystemState, error) {
	if ctx == nil {
		return SystemState{}, ErrSystemStateUnavailable
	}
	if err := ctx.Err(); err != nil {
		return SystemState{}, err
	}
	foundationOnce.Do(func() {
		_, foundationErr = purego.Dlopen("/System/Library/Frameworks/Foundation.framework/Foundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
	})
	if foundationErr != nil {
		return SystemState{}, ErrSystemStateUnavailable
	}
	class := objc.GetClass("NSProcessInfo")
	if class == 0 {
		return SystemState{}, ErrSystemStateUnavailable
	}
	process := objc.ID(class).Send(objc.RegisterName("processInfo"))
	if process == 0 {
		return SystemState{}, ErrSystemStateUnavailable
	}
	lowPowerSelector := objc.RegisterName("isLowPowerModeEnabled")
	if objc.ID(class).Send(objc.RegisterName("instancesRespondToSelector:"), lowPowerSelector) == 0 {
		return SystemState{}, ErrSystemStateUnavailable
	}
	lowPower := objc.Send[uint8](process, lowPowerSelector) != 0
	if err := ctx.Err(); err != nil {
		return SystemState{}, err
	}
	return SystemState{Known: true, SaverEnabled: lowPower}, nil
}
