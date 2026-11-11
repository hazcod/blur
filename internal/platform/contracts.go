package platform

import "context"

// SecretState is shared by secret-store adapters so platform packages can
// report typed outcomes without importing the higher-level securestore.
type SecretState string

const (
	SecretFound       SecretState = "found"
	SecretNotFound    SecretState = "not_found"
	SecretLocked      SecretState = "locked"
	SecretCorrupt     SecretState = "corrupt"
	SecretDenied      SecretState = "denied"
	SecretUnavailable SecretState = "unavailable"
	SecretAmbiguous   SecretState = "ambiguous"
)

// SecretStoreError carries only a closed status; backend details and secret
// values must never appear in its formatting.
// Operation is an optional closed startup-stage label: read, create, or verify.
// BackendCode is an optional numeric OS status for diagnostics; neither field
// may contain backend strings or user-controlled values. Error formatting
// intentionally exposes only the closed State.
type SecretStoreError struct {
	State       SecretState
	Operation   string
	BackendCode int32
}

func (e *SecretStoreError) Error() string { return "platform.secret_store: " + string(e.State) }

// Every port reports its availability without exposing private content.
type CapabilityReporter interface {
	Capability(context.Context) (CapabilityReport, error)
}

// CaptureProbe is non-content metadata sufficient for policy preflight.
// TargetID is an opaque adapter-issued identity, never a title, URL, or hostname.
// It is 1–256 ASCII bytes: letters, digits, dot, underscore, colon, or hyphen.
// Adapters must not encode content or environment values in it.
type CaptureProbe struct {
	TargetID string
	AppID    string
	Domain   string
	Private  bool
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

// ActiveApplicationReporter exposes only the current app identifier for
// preference/status UI. It never reports titles, URLs, or captured content.
type ActiveApplicationReporter interface {
	ActiveApplicationID() (string, bool)
}

// CaptureSuspender releases passive platform subscriptions while capture is
// paused. Capability/Start may establish them again when the user resumes.
type CaptureSuspender interface {
	Suspend(context.Context) error
}

// CapturePermissionRequester may ask the operating system to show its own
// permission guidance. Capability checks remain non-interactive; a caller
// invokes this only after an explicit capture-enabled startup attempt.
type CapturePermissionRequester interface {
	RequestPermission(context.Context) error
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
