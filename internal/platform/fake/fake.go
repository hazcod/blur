// Package fake provides deterministic in-memory platform adapters for tests.
package fake

import (
	"context"

	"github.com/nielsgroen/blur/internal/platform"
)

func report(r platform.CapabilityReport, id string) platform.CapabilityReport {
	r.ID = id
	return r
}

func guard(ctx context.Context, r platform.CapabilityReport, id string) error {
	r = report(r, id)
	if r.State != platform.Available {
		return &platform.UnavailableError{Report: r}
	}
	return ctx.Err()
}

type CaptureSource struct {
	Report      platform.CapabilityReport
	ProbeResult platform.CaptureProbe
	NextResult  platform.CaptureEvent
	NextWait    <-chan platform.CaptureEvent
	Calls       int
}

var _ platform.CaptureSource = (*CaptureSource)(nil)

func (f *CaptureSource) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	return report(f.Report, "capture"), ctx.Err()
}
func (f *CaptureSource) Probe(ctx context.Context) (platform.CaptureProbe, error) {
	if err := guard(ctx, f.Report, "capture"); err != nil {
		return platform.CaptureProbe{}, err
	}
	f.Calls++
	return f.ProbeResult, nil
}
func (f *CaptureSource) Next(ctx context.Context) (platform.CaptureEvent, error) {
	if err := guard(ctx, f.Report, "capture"); err != nil {
		return platform.CaptureEvent{}, err
	}
	if f.NextWait != nil {
		select {
		case <-ctx.Done():
			return platform.CaptureEvent{}, ctx.Err()
		case event := <-f.NextWait:
			f.Calls++
			return event, nil
		}
	}
	f.Calls++
	return f.NextResult, nil
}

type TextTargets struct {
	Report      platform.CapabilityReport
	LeaseResult platform.TargetLease
	Calls       int
}

var _ platform.TextTargets = (*TextTargets)(nil)

func (f *TextTargets) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	return report(f.Report, "text_targets"), ctx.Err()
}
func (f *TextTargets) Lease(ctx context.Context) (platform.TargetLease, error) {
	if err := guard(ctx, f.Report, "text_targets"); err != nil {
		return platform.TargetLease{}, err
	}
	f.Calls++
	return f.LeaseResult, nil
}
func (f *TextTargets) Replace(ctx context.Context, _ platform.TargetLease, _ string) error {
	if err := guard(ctx, f.Report, "text_targets"); err != nil {
		return err
	}
	f.Calls++
	return nil
}

type GestureHotkeys struct {
	Report     platform.CapabilityReport
	NextResult platform.GestureEvent
	Calls      int
}

var _ platform.GestureHotkeys = (*GestureHotkeys)(nil)

func (f *GestureHotkeys) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	return report(f.Report, "gesture_hotkeys"), ctx.Err()
}
func (f *GestureHotkeys) Next(ctx context.Context) (platform.GestureEvent, error) {
	if err := guard(ctx, f.Report, "gesture_hotkeys"); err != nil {
		return platform.GestureEvent{}, err
	}
	f.Calls++
	return f.NextResult, nil
}

type SurfaceManager struct {
	Report platform.CapabilityReport
	Calls  int
}

var _ platform.SurfaceManager = (*SurfaceManager)(nil)

func (f *SurfaceManager) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	return report(f.Report, "surface_manager"), ctx.Err()
}
func (f *SurfaceManager) Show(ctx context.Context, _ platform.SurfaceRequest) error {
	if err := guard(ctx, f.Report, "surface_manager"); err != nil {
		return err
	}
	f.Calls++
	return nil
}

type AudioRecorder struct {
	Report platform.CapabilityReport
	Chunk  []byte
	Calls  int
}

var _ platform.AudioRecorder = (*AudioRecorder)(nil)

func (f *AudioRecorder) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	return report(f.Report, "audio_recorder"), ctx.Err()
}
func (f *AudioRecorder) Start(ctx context.Context) error {
	if err := guard(ctx, f.Report, "audio_recorder"); err != nil {
		return err
	}
	f.Calls++
	return nil
}
func (f *AudioRecorder) Read(ctx context.Context, maxBytes int) ([]byte, error) {
	if err := guard(ctx, f.Report, "audio_recorder"); err != nil {
		return nil, err
	}
	if maxBytes < 0 || len(f.Chunk) > maxBytes {
		return nil, platform.ErrReadLimit
	}
	f.Calls++
	return append([]byte(nil), f.Chunk...), nil
}
func (f *AudioRecorder) Stop(ctx context.Context) error {
	if err := guard(ctx, f.Report, "audio_recorder"); err != nil {
		return err
	}
	f.Calls++
	return nil
}

type SecretStore struct {
	Report platform.CapabilityReport
	Value  []byte
	Calls  int
}

var _ platform.SecretStore = (*SecretStore)(nil)

func (f *SecretStore) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	return report(f.Report, "secret_store"), ctx.Err()
}
func (f *SecretStore) Get(ctx context.Context, _ string, maxBytes int) ([]byte, error) {
	if err := guard(ctx, f.Report, "secret_store"); err != nil {
		return nil, err
	}
	if maxBytes < 0 || len(f.Value) > maxBytes {
		return nil, platform.ErrReadLimit
	}
	f.Calls++
	return append([]byte(nil), f.Value...), nil
}
func (f *SecretStore) Put(ctx context.Context, _ string, v []byte) error {
	if err := guard(ctx, f.Report, "secret_store"); err != nil {
		return err
	}
	f.Calls++
	f.Value = append([]byte(nil), v...)
	return nil
}
func (f *SecretStore) Delete(ctx context.Context, _ string) error {
	if err := guard(ctx, f.Report, "secret_store"); err != nil {
		return err
	}
	f.Calls++
	f.Value = nil
	return nil
}

type LocalTransport struct {
	Report platform.CapabilityReport
	Calls  int
}

var _ platform.LocalTransport = (*LocalTransport)(nil)

func (f *LocalTransport) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	return report(f.Report, "local_transport"), ctx.Err()
}
func (f *LocalTransport) Start(ctx context.Context) error {
	if err := guard(ctx, f.Report, "local_transport"); err != nil {
		return err
	}
	f.Calls++
	return nil
}
func (f *LocalTransport) Stop(ctx context.Context) error {
	if err := guard(ctx, f.Report, "local_transport"); err != nil {
		return err
	}
	f.Calls++
	return nil
}

type PrivatePath struct {
	Report platform.CapabilityReport
	Path   string
	Calls  int
}

var _ platform.PrivatePath = (*PrivatePath)(nil)

func (f *PrivatePath) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	return report(f.Report, "private_path"), ctx.Err()
}
func (f *PrivatePath) Ensure(ctx context.Context, _ string) (string, error) {
	if err := guard(ctx, f.Report, "private_path"); err != nil {
		return "", err
	}
	f.Calls++
	return f.Path, nil
}

type Autostart struct {
	Report platform.CapabilityReport
	Calls  int
}

var _ platform.Autostart = (*Autostart)(nil)

func (f *Autostart) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	return report(f.Report, "autostart"), ctx.Err()
}
func (f *Autostart) SetEnabled(ctx context.Context, _ bool) error {
	if err := guard(ctx, f.Report, "autostart"); err != nil {
		return err
	}
	f.Calls++
	return nil
}

type SessionState struct {
	Report   platform.CapabilityReport
	Snapshot platform.SessionSnapshot
	Calls    int
}

var _ platform.SessionState = (*SessionState)(nil)

func (f *SessionState) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	return report(f.Report, "session_state"), ctx.Err()
}
func (f *SessionState) Current(ctx context.Context) (platform.SessionSnapshot, error) {
	if err := guard(ctx, f.Report, "session_state"); err != nil {
		return platform.SessionSnapshot{}, err
	}
	f.Calls++
	return f.Snapshot, nil
}
