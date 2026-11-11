package client

import (
	"context"
	"crypto/rand"
	"image"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hazcod/blur/internal/platform"
)

// uiSnapshotEnv names a developer-only mode: when set to a directory the app
// photographs its own windows into it, then quits. It is inert when unset.
const uiSnapshotEnv = "BLUR_UI_SNAPSHOT_DIR"

const uiSnapshotSettle = 700 * time.Millisecond

// uiSnapshotDir returns the requested output directory, or "" when the mode is
// off or unsupported on this OS.
func uiSnapshotDir() string {
	if !uiSnapshotSupported {
		return ""
	}
	return os.Getenv(uiSnapshotEnv)
}

// memorySecretStore keeps snapshot-mode keys in process memory so the mode
// never reads or writes the user's Keychain.
type memorySecretStore struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (m *memorySecretStore) Capability(context.Context) (platform.CapabilityReport, error) {
	return platform.CapabilityReport{}, nil
}

func (m *memorySecretStore) Get(_ context.Context, key string, maxBytes int) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[key]
	if !ok {
		return nil, &platform.SecretStoreError{State: platform.SecretNotFound}
	}
	if maxBytes < 0 || len(v) > maxBytes {
		return nil, platform.ErrReadLimit
	}
	return append([]byte(nil), v...), nil
}

func (m *memorySecretStore) Put(_ context.Context, key string, v []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.values == nil {
		m.values = map[string][]byte{}
	}
	m.values[key] = append([]byte(nil), v...)
	return nil
}

func (m *memorySecretStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, key)
	return nil
}

// openUISnapshotApp opens an empty throwaway store in a fresh temporary
// directory with an in-memory key. The real data directory, its instance lock,
// and the Keychain are never touched; the directory is removed on Close.
func openUISnapshotApp(ctx context.Context) (*LocalApp, error) {
	directory, err := os.MkdirTemp("", "blur-ui-snapshot-")
	if err != nil {
		return nil, ErrNotConfigured
	}
	app, err := openLocalAppAt(ctx, filepath.Join(directory, "Blur"), &memorySecretStore{}, rand.Reader)
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	release := app.releaseLock
	app.releaseLock = func() {
		if release != nil {
			release()
		}
		_ = os.RemoveAll(directory)
	}
	return app, nil
}

// startUISnapshotIfRequested schedules the snapshot run on the GUI thread. It
// does nothing unless BLUR_UI_SNAPSHOT_DIR is set.
func (d *desktop) startUISnapshotIfRequested() {
	dir := uiSnapshotDir()
	if d == nil || d.app == nil || dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		slog.Error("ui snapshot directory unavailable", "result", "failed")
		return
	}
	// Snapshot mode never captures the user's screen or shows menu items.
	d.preferences.CaptureEnabled = false
	d.preferences.MenuBarItemEnabled = false
	d.dashboardOpen = true
	d.quickUI.snapshotRun = true
	steps := []func() (name string, window string){}
	for _, page := range desktopPages() {
		steps = append(steps, func() (string, string) {
			if page == "Settings" {
				d.settingsGroup = settingsGroupCapture
			}
			d.showSnapshotPage(page)
			return page, "main"
		})
	}
	steps = append(steps, func() (string, string) {
		d.openQuickPanel()
		return "quick-panel", "quick"
	})
	steps = append(steps, d.uiSnapshotQuickSteps()...)
	for _, group := range uiSnapshotSettingsGroups() {
		steps = append(steps, func() (string, string) {
			d.settingsGroup = group.name
			d.showSnapshotPage("Settings")
			return group.file, "main"
		})
	}
	d.runUISnapshotSteps(dir, steps)
}

// uiSnapshotSettingsGroup names one Settings group snapshot (Capture is
// already covered by the plain Settings page step).
type uiSnapshotSettingsGroup struct{ file, name string }

func uiSnapshotSettingsGroups() []uiSnapshotSettingsGroup {
	return []uiSnapshotSettingsGroup{
		{"Settings-ai", settingsGroupAI},
		{"Settings-quick-ask", settingsGroupQuickAsk},
		{"Settings-connections", settingsGroupConnections},
		{"Settings-memory", settingsGroupMemory},
		{"Settings-privacy", settingsGroupPrivacy},
	}
}

func (d *desktop) showSnapshotPage(page string) {
	d.page, d.status = page, ""
	if page == "Settings" && d.powerStatus == "" && !d.powerChecking {
		d.refreshPowerState()
	}
	if page == "Library" && !d.libraryLoaded && !d.libraryLoading {
		d.loadLibrary()
	}
	d.app.RequestUpdate()
}

// runUISnapshotSteps prepares each view, lets it settle, snapshots its window,
// and quits after the last step. Every callback runs on the GUI thread.
func (d *desktop) runUISnapshotSteps(dir string, steps []func() (string, string)) {
	var run func(i int)
	run = func(i int) {
		if i >= len(steps) {
			d.app.Quit()
			return
		}
		name, windowID := steps[i]()
		settle := uiSnapshotSettle
		if windowID == "quick" && name != "quick-panel" {
			settle = uiSnapshotQuickSettle
		}
		d.app.TimeoutFunc(settle, func() {
			d.captureSnapshotStep(dir, name, windowID, func() { run(i + 1) })
		})
	}
	d.app.TimeoutFunc(uiSnapshotSettle, func() { run(0) })
}

// captureSnapshotStep shows the window through GoUI's normal show path (so its
// content has been laid out and painted), forces a paint, waits about two
// frames and captures. A uniform image gets one retry after another update
// turn and a 300 ms wait; a still-uniform image is kept and logged.
func (d *desktop) captureSnapshotStep(dir, name, windowID string, next func()) {
	window := d.app.FindWindow(windowID)
	if window == nil {
		slog.Warn("ui snapshot failed", "view", name, "result", "failed")
		next()
		return
	}
	if windowID == "main" {
		// Never let the quick panel occlude a page capture.
		if quick := d.app.FindWindow("quick"); quick != nil {
			_ = quick.PlatformWindow().Hide()
		}
	}
	_ = window.Show()
	if windowID == "quick" && d.quickUI.snapshotRun {
		_ = orderWindowFrontQuietly(window.PlatformWindow().NativeHandle())
	}
	path := filepath.Join(dir, name+".png")
	capture := func() error {
		handle := window.PlatformWindow().NativeHandle()
		displayWindowNow(handle)
		return snapshotWindowToFile(handle, path)
	}
	finish := func(err error) {
		if err != nil {
			slog.Warn("ui snapshot failed", "view", name, "result", "failed")
		} else {
			slog.Info("ui snapshot written", "view", name, "result", "ok")
		}
		next()
	}
	d.app.RequestUpdate()
	d.app.TimeoutFunc(2*uiSnapshotFrame, func() {
		err := capture()
		if err != nil || !snapshotFileIsUniform(path) {
			finish(err)
			return
		}
		d.app.RequestUpdate()
		d.app.TimeoutFunc(uiSnapshotRetryWait, func() {
			err := capture()
			if err == nil && snapshotFileIsUniform(path) {
				slog.Warn("ui snapshot blank", "view", name)
			}
			finish(err)
		})
	})
}

const (
	uiSnapshotFrame     = 30 * time.Millisecond
	uiSnapshotRetryWait = 300 * time.Millisecond
)

func snapshotFileIsUniform(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	img, err := png.Decode(file)
	return err == nil && isUniformImage(img)
}

// isUniformImage reports whether a 5x5 grid of samples are all one colour,
// which is what a window that never painted looks like.
func isUniformImage(img image.Image) bool {
	if img == nil {
		return false
	}
	bounds := img.Bounds()
	if bounds.Empty() {
		return false
	}
	var first [4]uint32
	for row := 0; row < 5; row++ {
		for col := 0; col < 5; col++ {
			x := bounds.Min.X + (bounds.Dx()-1)*col/4
			y := bounds.Min.Y + (bounds.Dy()-1)*row/4
			r, g, b, a := img.At(x, y).RGBA()
			sample := [4]uint32{r, g, b, a}
			if row == 0 && col == 0 {
				first = sample
			} else if sample != first {
				return false
			}
		}
	}
	return true
}
