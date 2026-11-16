package client

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/hazcod/blur/internal/platform/darwin"
)

// Loading is lazy: readable AX windows neither load Vision nor require Screen
// Recording. This bridge can only open the signed library in this app bundle.
type bundledVisualText struct {
	mu     sync.Mutex
	bridge *darwin.ScreenTextBridge
	closed bool
}

func (b *bundledVisualText) open() (*darwin.ScreenTextBridge, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, darwin.ErrScreenTextUnavailable
	}
	if b.bridge != nil {
		return b.bridge, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, darwin.ErrScreenTextUnavailable
	}
	bridge, err := darwin.OpenScreenTextBridge(filepath.Join(filepath.Dir(executable), "..", "Frameworks", "libBlurScreenText.dylib"))
	if err != nil {
		return nil, err
	}
	b.bridge = bridge
	return bridge, nil
}
func (b *bundledVisualText) TopmostWindow(ctx context.Context) (darwin.ScreenTextWindow, error) {
	if err := ctx.Err(); err != nil {
		return darwin.ScreenTextWindow{}, err
	}
	bridge, err := b.open()
	if err != nil {
		return darwin.ScreenTextWindow{}, err
	}
	return bridge.TopmostWindow(ctx)
}
func (b *bundledVisualText) RecognizeWindow(ctx context.Context, request darwin.ScreenTextRequest) (darwin.ScreenTextResult, error) {
	if err := ctx.Err(); err != nil {
		return darwin.ScreenTextResult{}, err
	}
	bridge, err := b.open()
	if err != nil {
		return darwin.ScreenTextResult{}, err
	}
	return bridge.RecognizeWindow(ctx, request)
}
func (b *bundledVisualText) Close() error {
	b.mu.Lock()
	b.closed = true
	bridge := b.bridge
	b.bridge = nil
	b.mu.Unlock()
	if bridge != nil {
		return bridge.Close()
	}
	return nil
}
