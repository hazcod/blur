package platform

import "context"

// Every port reports its availability without exposing private content.
type CapabilityReporter interface {
	Capability(context.Context) (CapabilityReport, error)
}

// CaptureProbe is non-content metadata sufficient for policy preflight.
// TargetID is an opaque adapter-issued identity, never a title, URL, or hostname.
type CaptureProbe struct {
	TargetID string
	Secure   bool
	Elevated bool
}
type CaptureEvent struct {
	Kind     string
	TargetID string
}

type CaptureSource interface {
	CapabilityReporter
	Probe(context.Context) (CaptureProbe, error)
	Next(context.Context) (CaptureEvent, error)
}

// TargetLease is an opaque handle; adapters must revalidate it before writing.
type TargetLease struct{ ID string }
type TextTargets interface {
	CapabilityReporter
	Lease(context.Context) (TargetLease, error)
	Replace(context.Context, TargetLease, string) error
}

type GestureEvent struct{ Kind string }
type GestureHotkeys interface {
	CapabilityReporter
	Next(context.Context) (GestureEvent, error)
}

type SurfaceRequest struct{ Kind string }
type SurfaceManager interface {
	CapabilityReporter
	Show(context.Context, SurfaceRequest) error
}

type AudioRecorder interface {
	CapabilityReporter
	Start(context.Context) error
	Read(context.Context, int) ([]byte, error)
	Stop(context.Context) error
}

type SecretStore interface {
	CapabilityReporter
	Get(context.Context, string, int) ([]byte, error)
	Put(context.Context, string, []byte) error
	Delete(context.Context, string) error
}

// LocalTransport owns only availability and lifecycle here. Authentication and
// concrete listen/dial operations belong to internal/localapi.
type LocalTransport interface {
	CapabilityReporter
	Start(context.Context) error
	Stop(context.Context) error
}

type PrivatePath interface {
	CapabilityReporter
	Ensure(context.Context, string) (string, error)
}

type Autostart interface {
	CapabilityReporter
	SetEnabled(context.Context, bool) error
}

type SessionSnapshot struct {
	Locked bool
	Idle   bool
}
type SessionState interface {
	CapabilityReporter
	Current(context.Context) (SessionSnapshot, error)
}
