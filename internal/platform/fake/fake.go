// Package fake provides deterministic in-memory platform adapters for tests.
package fake

import (
	"context"

	"github.com/hazcod/blur/internal/platform"
)

func report(r platform.CapabilityReport, id string) (platform.CapabilityReport, bool) {
	wrongID := r.ID != "" && r.ID != id
	r.ID = id
	if wrongID || r.Validate() != nil {
		return platform.CapabilityReport{ID: id, State: platform.TemporarilyUnresponsive, Reason: "invalid_report", Evidence: "fake configuration rejected"}, false
	}
	return r, true
}

func capability(ctx context.Context, configured platform.CapabilityReport, id string) (platform.CapabilityReport, error) {
	r, valid := report(configured, id)
	if err := ctx.Err(); err != nil {
		return r, err
	}
	if !valid {
		return r, platform.ErrInvalidCapabilityReport
	}
	return r, nil
}

func guard(ctx context.Context, r platform.CapabilityReport, id string) error {
	r, _ = report(r, id)
	if r.State != platform.Available {
		return &platform.UnavailableError{Report: r}
	}
	return ctx.Err()
}

type CaptureSource struct {
	Report      platform.CapabilityReport
	ProbeResult platform.CaptureProbe
	ProbeError  error
	NextResult  platform.CaptureEvent
	NextWait    <-chan platform.CaptureEvent
	Calls       int
}

var _ platform.CaptureSource = (*CaptureSource)(nil)

func (f *CaptureSource) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	return capability(ctx, f.Report, "capture")
}
func (f *CaptureSource) Probe(ctx context.Context) (platform.CaptureProbe, error) {
	if err := guard(ctx, f.Report, "capture"); err != nil {
		return platform.CaptureProbe{}, err
	}
	f.Calls++
	if f.ProbeError != nil {
		return platform.CaptureProbe{}, f.ProbeError
	}
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
	issued      string
}

var _ platform.TextTargets = (*TextTargets)(nil)

func (f *TextTargets) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	return capability(ctx, f.Report, "text_targets")
}
func (f *TextTargets) Lease(ctx context.Context) (platform.TargetLease, error) {
	if err := guard(ctx, f.Report, "text_targets"); err != nil {
		return platform.TargetLease{}, err
	}
	f.Calls++
	f.issued = f.LeaseResult.ID
	return f.LeaseResult, nil
}
func (f *TextTargets) Replace(ctx context.Context, lease platform.TargetLease, _ string) error {
	if err := guard(ctx, f.Report, "text_targets"); err != nil {
		return err
	}
	if lease.ID == "" || lease.ID != f.issued || lease.ID != f.LeaseResult.ID {
		return platform.ErrInvalidLease
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
	return capability(ctx, f.Report, "gesture_hotkeys")
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
	return capability(ctx, f.Report, "surface_manager")
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
	return capability(ctx, f.Report, "audio_recorder")
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
	Calls  int
	values map[string][]byte
}

var _ platform.SecretStore = (*SecretStore)(nil)

func (f *SecretStore) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	return capability(ctx, f.Report, "secret_store")
}
func (f *SecretStore) Get(ctx context.Context, key string, maxBytes int) ([]byte, error) {
	if err := guard(ctx, f.Report, "secret_store"); err != nil {
		return nil, err
	}
	v := f.values[key]
	if maxBytes < 0 || len(v) > maxBytes {
		return nil, platform.ErrReadLimit
	}
	f.Calls++
	return append([]byte(nil), v...), nil
}
func (f *SecretStore) Put(ctx context.Context, key string, v []byte) error {
	if err := guard(ctx, f.Report, "secret_store"); err != nil {
		return err
	}
	f.Calls++
	if f.values == nil {
		f.values = make(map[string][]byte)
	}
	f.values[key] = append([]byte(nil), v...)
	return nil
}
func (f *SecretStore) Delete(ctx context.Context, key string) error {
	if err := guard(ctx, f.Report, "secret_store"); err != nil {
		return err
	}
	f.Calls++
	delete(f.values, key)
	return nil
}

type LocalTransport struct {
	Report platform.CapabilityReport
	Calls  int
}

var _ platform.LocalTransport = (*LocalTransport)(nil)

func (f *LocalTransport) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	return capability(ctx, f.Report, "local_transport")
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
	return capability(ctx, f.Report, "private_path")
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
	return capability(ctx, f.Report, "autostart")
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
	return capability(ctx, f.Report, "session_state")
}
func (f *SessionState) Current(ctx context.Context) (platform.SessionSnapshot, error) {
	if err := guard(ctx, f.Report, "session_state"); err != nil {
		return platform.SessionSnapshot{}, err
	}
	f.Calls++
	return f.Snapshot, nil
}
