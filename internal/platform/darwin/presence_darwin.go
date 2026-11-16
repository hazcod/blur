package darwin

import (
	"context"
	"errors"
	"math"
	"sync"

	"github.com/ebitengine/purego"
)

const captureIdleSeconds = 180

func (a *Adapter) UserIdle(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if a == nil || a.idleSeconds == nil {
		return false, nil
	}
	seconds, err := a.idleSeconds()
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return false, errors.New("darwin: idle state unavailable")
	}
	return seconds >= captureIdleSeconds, ctx.Err()
}

var idleAPI struct {
	once    sync.Once
	seconds func(int32, uint32) float64
	err     error
}

func nativeIdleSeconds() (float64, error) {
	idleAPI.once.Do(func() {
		framework, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			idleAPI.err = errors.New("darwin: idle state unavailable")
			return
		}
		purego.RegisterLibFunc(&idleAPI.seconds, framework, "CGEventSourceSecondsSinceLastEventType")
	})
	if idleAPI.err != nil {
		return 0, idleAPI.err
	}
	return idleAPI.seconds(0, ^uint32(0)), nil // combined session state; any input event
}
