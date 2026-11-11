//go:build windows

package windows

import (
	"context"

	"github.com/hazcod/blur/internal/platform"
)

// New returns the Windows capture boundary. UI Automation is read only after
// explicit user start and passes through the shared bounded sanitizer.
func New() *Adapter { return newAdapter(nativeBackend{}) }

type nativeBackend struct{}

func (nativeBackend) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	return windowsCaptureCapability(ctx)
}

func (nativeBackend) Foreground(ctx context.Context) (FocusedTarget, error) {
	return foregroundTarget(ctx)
}

func (nativeBackend) Subscribe(ctx context.Context) (eventSubscription, error) {
	return newNativeEventSubscription(ctx)
}
