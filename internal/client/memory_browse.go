package client

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/storage"
)

// Each directory keeps its own opaque continuation. A finished directory must
// not restart while another directory still has pages.
type memoryBrowseCursor struct {
	observation, capture                         string
	meeting                                      uint64
	observationsDone, capturesDone, meetingsDone bool
	noteAt, recentAt, recentStart, recentEnd     time.Time
	noteID, recentSource                         string
	notesDone, recentDone                        bool
	notesCatchingUp                              bool
}

type memoryBrowseItem struct {
	SearchHit
	meeting *meetings.Meeting
}

type memoryBrowsePage struct {
	items           []memoryBrowseItem
	next            memoryBrowseCursor
	more, pending   bool
	notesCatchingUp bool
}

type observationPageReader interface {
	ListObservationPage(context.Context, storage.ObservationPageRequest) (storage.ObservationPage, error)
}

type noteDocumentPageReader interface {
	ListNoteDocumentsPage(context.Context, notes.DocumentPageRequest) (notes.DocumentPage, error)
}

// browseSavedMemory uses the existing encrypted directories. It never starts
// capture, inference, or a scan of all saved content.
func (c *Client) browseSavedMemory(ctx context.Context, after memoryBrowseCursor) (memoryBrowsePage, error) {
	if c == nil || c.data == nil || ctx == nil {
		return memoryBrowsePage{}, ErrInvalidRequest
	}
	op, done, err := c.beginOperation(ctx, shortOperationTimeout)
	if err != nil {
		return memoryBrowsePage{}, err
	}
	defer done()
	op, cancel := context.WithCancel(op)
	defer cancel()
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return memoryBrowsePage{}, ErrMemoryChanging
	}
	id, stable := c.registerAskEpoch(cancel, &epoch)
	if !stable {
		return memoryBrowsePage{}, ErrMemoryChanging
	}
	defer c.unregisterAsk(id)
	page := memoryBrowsePage{next: after, notesCatchingUp: after.notesCatchingUp}
	_, noteDocuments := c.data.(noteDocumentPageReader)
	_, recentSources := c.data.(ObservationWindowReader)
	if recentSources && page.next.recentStart.IsZero() {
		page.next.recentEnd = time.Now().UTC().Add(time.Nanosecond)
		page.next.recentStart = page.next.recentEnd.Add(-30 * 24 * time.Hour)
	}
	appendSource := func(source capture.Evidence, at time.Time) error {
		if source.Validate() != nil {
			return ErrInvalidRequest
		}
		// Voice notes have one entry per session, with a paged transcript.
		if source.AppID() == "sh.blur.meetings" && c.CanUseMeetings() || strings.EqualFold(source.AppID(), appidentity.BundleID) {
			return nil
		}
		if noteDocuments && source.AppID() == "sh.blur.notes" {
			return nil
		}
		at, unknown, err := c.sourceCaptureTime(op, source.SourceID(), at)
		if err != nil {
			return err
		}
		page.items = append(page.items, memoryBrowseItem{SearchHit: SearchHit{SourceID: source.SourceID(), AppID: source.AppID(), Domain: source.Domain(), Text: source.Text(), CapturedAt: at, TimeUnknown: unknown, Partial: source.Truncated(), Truncated: source.Truncated()}})
		return nil
	}
	appendOlderSource := func(source capture.Evidence, at time.Time) error {
		if source.AppID() == "sh.blur.meetings" && c.CanUseMeetings() || source.AppID() == "sh.blur.notes" && noteDocuments || strings.EqualFold(source.AppID(), appidentity.BundleID) {
			return nil
		}
		if recentSources {
			if sightings, ok := c.data.(interface {
				SourceEvidenceSightingAt(context.Context, string, time.Time, time.Time) (time.Time, bool, error)
			}); ok {
				_, found, err := sightings.SourceEvidenceSightingAt(op, source.SourceID(), page.next.recentStart, page.next.recentEnd)
				if err != nil {
					return err
				}
				if found {
					return nil
				}
			} else {
				knownAt, unknown, err := c.sourceCaptureTime(op, source.SourceID(), at)
				if err != nil {
					return err
				}
				if !unknown && !knownAt.IsZero() && !knownAt.Before(page.next.recentStart) && knownAt.Before(page.next.recentEnd) {
					return nil
				}
			}
		}
		return appendSource(source, at)
	}
	if !after.notesDone {
		page.next.notesDone = true
		if reader, ok := c.data.(noteDocumentPageReader); ok {
			result, err := reader.ListNoteDocumentsPage(op, notes.DocumentPageRequest{BeforeUpdatedAt: after.noteAt, BeforeID: after.noteID, Limit: 12, MaxBytes: MaxSearchBytes})
			if err != nil {
				return memoryBrowsePage{}, err
			}
			if len(result.Documents) > 12 || result.HasMore && (result.NextUpdatedAt.IsZero() || result.NextID == "") {
				return memoryBrowsePage{}, ErrInvalidRequest
			}
			for _, document := range result.Documents {
				source, found, err := storage.ReadSourceEvidence(op, c.data, document.SourceID)
				if err != nil {
					return memoryBrowsePage{}, err
				}
				if !found || source.AppID() != "sh.blur.notes" || source.Text() != document.Text {
					return memoryBrowsePage{}, ErrMemoryChanging
				}
				page.items = append(page.items, memoryBrowseItem{SearchHit: SearchHit{SourceID: source.SourceID(), AppID: source.AppID(), Domain: source.Domain(), Text: source.Text(), CapturedAt: document.UpdatedAt}})
			}
			page.next.noteAt, page.next.noteID, page.next.notesDone = result.NextUpdatedAt, result.NextID, !result.HasMore
			page.notesCatchingUp, page.next.notesCatchingUp = result.CatchingUp, result.CatchingUp
		}
	}
	if recentSources && !after.recentDone {
		reader := c.data.(ObservationWindowReader)
		excluded := []string{appidentity.BundleID, "sh.blur.meetings"}
		if noteDocuments {
			excluded = append(excluded, "sh.blur.notes")
		}
		result, err := readSourceWindow(op, reader, storage.ObservationWindowRequest{Start: page.next.recentStart, End: page.next.recentEnd, BeforeCapturedAt: after.recentAt, BeforeSourceID: after.recentSource, ExcludeAppIDs: excluded, Limit: 12, MaxBytes: MaxSearchBytes})
		if err != nil {
			return memoryBrowsePage{}, err
		}
		for i, source := range result.Sources {
			if err := appendSource(source, result.CapturedAt[i]); err != nil {
				return memoryBrowsePage{}, err
			}
		}
		page.next.recentAt, page.next.recentSource, page.next.recentDone = result.NextCapturedAt, result.NextSourceID, !result.HasMore
		page.pending = page.pending || result.Pending
	} else if !recentSources {
		page.next.recentDone = true
	}
	if !after.meetingsDone {
		page.next.meetingsDone = true
		if c.CanUseMeetings() {
			result, err := c.meetings.repo.ListMeetings(op, meetings.PageRequest{After: after.meeting, Limit: 12})
			if err != nil {
				return memoryBrowsePage{}, err
			}
			page.next.meeting, page.next.meetingsDone = result.Next, !result.More
			for _, saved := range result.Meetings {
				if saved.Validate() != nil {
					return memoryBrowsePage{}, ErrInvalidRequest
				}
				item := saved
				page.items = append(page.items, memoryBrowseItem{SearchHit: SearchHit{AppID: "sh.blur.meetings", Text: item.Title, CapturedAt: item.StartedAt}, meeting: &item})
			}
		}
	}
	if !after.observationsDone {
		page.next.observationsDone = true
		if reader, ok := c.data.(observationPageReader); ok {
			for skipped := 0; skipped < 8; skipped++ {
				before, itemCount := page.next.observation, len(page.items)
				result, err := reader.ListObservationPage(op, storage.ObservationPageRequest{After: before, Limit: 12, MaxBytes: MaxSearchBytes})
				if err != nil {
					return memoryBrowsePage{}, err
				}
				if len(result.Observations) > 12 || len(result.CapturedAt) != len(result.Observations) {
					return memoryBrowsePage{}, ErrInvalidRequest
				}
				for i, observation := range result.Observations {
					source, err := capture.ObservationEvidence(observation)
					if err != nil {
						return memoryBrowsePage{}, ErrInvalidRequest
					}
					if err := appendOlderSource(source, result.CapturedAt[i]); err != nil {
						return memoryBrowsePage{}, err
					}
					page.next.observation = observation.ContentID()
				}
				if result.HasMore && page.next.observation == before {
					return memoryBrowsePage{}, ErrInvalidRequest
				}
				page.next.observationsDone = !result.HasMore
				if !result.HasMore || len(page.items) > itemCount {
					break
				}
			}
		}
	}
	if !after.capturesDone {
		page.next.capturesDone = true
		if reader, ok := c.data.(storage.CaptureEvidencePageReader); ok {
			result, err := reader.ListCaptureEvidencePage(op, storage.CaptureEvidencePageRequest{After: after.capture, Limit: 12, MaxBytes: 512 << 10})
			if err != nil {
				return memoryBrowsePage{}, err
			}
			if len(result.Sources) > 12 || len(result.CapturedAt) != len(result.Sources) {
				return memoryBrowsePage{}, ErrInvalidRequest
			}
			for i, source := range result.Sources {
				if err := appendOlderSource(source, result.CapturedAt[i]); err != nil {
					return memoryBrowsePage{}, err
				}
			}
			page.next.capture, page.next.capturesDone = result.Next, !result.HasMore
			page.pending = page.pending || result.Pending
		}
	}
	if err := op.Err(); err != nil {
		return memoryBrowsePage{}, err
	}
	if !c.MemoryReadCurrent(epoch) {
		return memoryBrowsePage{}, ErrMemoryChanging
	}
	page.more = !page.next.meetingsDone || !page.next.observationsDone || !page.next.capturesDone || !page.next.notesDone || !page.next.recentDone
	sort.SliceStable(page.items, func(i, j int) bool { return page.items[i].CapturedAt.After(page.items[j].CapturedAt) })
	return page, nil
}

func sourceKind(hit SearchHit) string {
	switch {
	case hit.AppID == "sh.blur.meetings":
		return "Voice note"
	case hit.AppID == "sh.blur.notes":
		return "Note"
	case hit.Task != nil:
		return "Task"
	case hit.Domain != "":
		return "Page"
	default:
		return "Capture"
	}
}
