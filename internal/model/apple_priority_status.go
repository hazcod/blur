package model

import "context"

// AppleInferenceSnapshot is content-free, process-local admission telemetry.
// Active refers to the actual held helper slot, including retirement after a
// preemption; it never claims model-token progress or a completed cancellation.
type AppleInferenceSnapshot struct {
	Active             string
	Group              string
	Retiring           bool
	InteractiveWaiting int
	BackgroundWaiting  int
	WaitingGroups      map[string]int
}
type backgroundInferenceGroupKey struct{}

// WithBackgroundInferenceGroup adds a closed job-group label for telemetry.
// Unknown values become "other"; no prompt/source-derived labels are retained.
func WithBackgroundInferenceGroup(ctx context.Context, group string) context.Context {
	ctx = WithBackgroundInference(ctx)
	if ctx == nil {
		return nil
	}
	switch group {
	case "library", "recaps", "notes":
	default:
		group = "other"
	}
	return context.WithValue(ctx, backgroundInferenceGroupKey{}, group)
}
func AppleInferenceStatus() AppleInferenceSnapshot { return appleInferenceGate.snapshot() }
func (g *applePriorityGate) snapshot() AppleInferenceSnapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := AppleInferenceSnapshot{Active: "idle", InteractiveWaiting: g.interactiveWaiting, BackgroundWaiting: g.backgroundWaiting, WaitingGroups: make(map[string]int, len(g.waitingGroups))}
	if g.active != nil {
		out.Active = "interactive"
		if g.active.background {
			out.Active = "background"
			out.Group = g.active.group
		}
		out.Retiring = g.active.retiring
	}
	for group, count := range g.waitingGroups {
		out.WaitingGroups[group] = count
	}
	return out
}
