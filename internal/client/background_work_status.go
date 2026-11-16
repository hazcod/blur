package client

import (
	"time"

	"github.com/hazcod/blur/internal/model"
)

// BackgroundWorkSnapshot contains only bounded, process-local worker metadata.
// Reads perform no model, native, power probe, file or repository operation.
// Counts describe known jobs, never percent complete or estimated remaining work.
type BackgroundWorkSnapshot struct {
	Groups []BackgroundWorkGroup
	Apple  model.AppleInferenceSnapshot
}

// BackgroundWorkGroup describes observed work, not inferred progress. Running
// includes a worker awaiting native admission. Library Queued counts known
// incomplete incremental sources, including its active source; the other groups
// exclude active jobs. Completion history lasts for the current worker lifetime
// and can reset when AI configuration replaces a worker.
type BackgroundWorkGroup struct {
	ID                         string
	Enabled                    bool
	State                      string // disabled, idle, running, queued, deferred, unavailable
	Reason                     string // Closed reason; never a backend error message.
	Queued, Running, Completed int
	LastCompletedAt            time.Time
	LastErrorCode              string
	LastErrorAt                time.Time
	CoverageLimited            bool
}
type noteWorkTelemetry struct {
	deferred        bool
	completed       int
	lastCompletedAt time.Time
	lastErrorCode   string
	lastErrorAt     time.Time
}

func (c *Client) BackgroundWorkStatus() BackgroundWorkSnapshot {
	out := BackgroundWorkSnapshot{Apple: model.AppleInferenceStatus(), Groups: []BackgroundWorkGroup{{ID: "library", State: "disabled"}, {ID: "recaps", State: "disabled"}, {ID: "notes", State: "disabled"}}}
	if c == nil {
		return out
	}
	var settings LocalAISettings
	repair := false
	notesAvailable := false
	if c.localAI != nil {
		c.localAI.mu.RLock()
		settings, repair = c.localAI.settings, c.localAI.needsRepair
		library, recaps, service := c.localAI.libraryWorker, c.localAI.chronicleWorker, c.localAI.service
		c.localAI.mu.RUnlock()
		out.Groups[0].Enabled = settings.Enabled && settings.AutomaticLibrary
		out.Groups[1].Enabled = settings.Enabled && settings.BackgroundSummaries
		out.Groups[2].Enabled = settings.Enabled && (!isRemoteCompletionProvider(effectiveProvider(settings)) || settings.RemoteNotesEnabled)
		for i := range out.Groups {
			if out.Groups[i].Enabled {
				out.Groups[i].State = "unavailable"
				out.Groups[i].Reason = "model_unavailable"
				if repair {
					out.Groups[i].Reason = "settings_invalid"
				}
			}
		}
		if library != nil {
			status := library.Status()
			group := &out.Groups[0]
			group.Queued = status.Queued
			group.Completed = status.ProcessedChunks
			group.LastCompletedAt = status.LastCompletedAt
			group.LastErrorCode = status.LastErrorCode
			group.LastErrorAt = status.LastErrorAt
			group.CoverageLimited = status.CoverageLimited
			if status.Active {
				group.Running = 1
			}
			if group.Enabled && !repair {
				switch {
				case status.DeferredReason != "":
					group.State = "deferred"
					group.Reason = status.DeferredReason
				case status.State == "unavailable":
					group.State = "unavailable"
					group.Reason = status.ErrorCode
				case status.Active:
					group.State = "running"
					group.Reason = ""
				case !status.Complete:
					group.State = "queued"
					group.Reason = ""
				default:
					group.State = "idle"
					group.Reason = ""
				}
			}
		}
		if recaps != nil {
			status := recaps.Status()
			group := &out.Groups[1]
			group.Queued, group.Running, group.Completed = status.Queued, status.Running, status.Completed
			group.LastCompletedAt, group.LastErrorCode, group.LastErrorAt = status.LastCompletedAt, status.LastErrorCode, status.LastErrorAt
			if group.Enabled && !repair {
				group.State, group.Reason = status.State, status.Reason
			}
		}
		notesAvailable = out.Groups[2].Enabled && !repair && service != nil && service.AIConfigured()
		if notesAvailable {
			out.Groups[2].State = "idle"
			out.Groups[2].Reason = ""
		}
		if !isLocalCompletionProvider(effectiveProvider(settings)) {
			for i := 0; i < 2; i++ {
				if out.Groups[i].Enabled {
					out.Groups[i].State = "unavailable"
					out.Groups[i].Reason = "local_model_required"
				}
			}
		}
	}
	c.noteProcessingMu.Lock()
	notes := c.noteProcessing
	c.noteProcessingMu.Unlock()
	if notes != nil {
		notes.mu.Lock()
		group := &out.Groups[2]
		group.Queued = len(notes.jobs)
		if notes.active != nil {
			group.Running = 1
		}
		group.Completed = notes.work.completed
		group.LastCompletedAt = notes.work.lastCompletedAt
		group.LastErrorCode = notes.work.lastErrorCode
		group.LastErrorAt = notes.work.lastErrorAt
		if group.Enabled && !repair && notesAvailable {
			switch {
			case notes.work.deferred:
				group.State = "deferred"
				group.Reason = "foreground"
			case notes.active != nil:
				group.State = "running"
				group.Reason = ""
			case len(notes.jobs) > 0:
				group.State = "queued"
				group.Reason = ""
			case notes.status.State == "unavailable":
				group.State = "unavailable"
				group.Reason = notes.status.ErrorCode
			default:
				group.State = "idle"
				group.Reason = ""
			}
		}
		notes.mu.Unlock()
	}
	if effectiveProvider(settings) == ProviderApple {
		for i := range out.Groups {
			group := &out.Groups[i]
			if !group.Enabled || repair || group.ID == "notes" && !notesAvailable {
				continue
			}
			retiring := out.Apple.Active == "background" && out.Apple.Group == group.ID && out.Apple.Retiring
			waiting := out.Apple.WaitingGroups[group.ID] > 0 && (out.Apple.Active == "interactive" || out.Apple.InteractiveWaiting > 0)
			if retiring || waiting {
				group.State = "deferred"
				group.Reason = "foreground"
			}
		}
	}
	for i := range out.Groups {
		out.Groups[i].Reason = backgroundStatusReason(out.Groups[i].Reason)
		out.Groups[i].LastErrorCode = backgroundStatusReason(out.Groups[i].LastErrorCode)
	}
	return out
}

func backgroundStatusReason(reason string) string {
	switch reason {
	case "", "foreground", "policy", "provider_unavailable", "settings_invalid", "model_unavailable", "local_model_required", "queue_full", "timeout", "source_unavailable", "suggestions_unavailable", "model_budget_exceeded", "model_refused", "model_configuration", "invalid_output", "local_processing_failed", "model_missing", "model_changed", "capacity", "processing_failed":
		return reason
	default:
		return "processing_failed"
	}
}
