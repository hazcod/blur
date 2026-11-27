package browsercapture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/ingest"
)

const MaxDOMBatchBytes = 32 << 10
const maxDocumentJobs = 4

var ErrDocumentRestart = errors.New("browser capture: restart document")

type documentJob struct {
	documentID        string
	revision, next    int64
	last              [32]byte
	touched           time.Time
	builder           *capture.CaptureBuilder
	summary           capture.CaptureSummary
	title             string
	limited, finished bool
	characters        int
}

type documentWork struct {
	cleanup          []string
	summary          capture.CaptureSummary
	committed, fresh bool
}

func validHex(value string, bytes int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == bytes && hex.EncodeToString(decoded) == value
}

func validateDocumentSnapshot(page Snapshot, now time.Time) (capture.SanitizedObservation, error) {
	if !validHex(page.DocumentID, 16) || !validHex(page.SourceKey, 32) || page.Revision < 1 || page.Sequence < 0 ||
		page.Revision > 1<<53-1 || page.Sequence > 1<<53-1 ||
		len(page.Text) > MaxDOMBatchBytes || !utf8.ValidString(page.Text) || strings.ContainsRune(page.Text, 0) || page.Truncated {
		return capture.SanitizedObservation{}, ErrRejected
	}
	u, err := url.Parse(page.URL)
	if err != nil || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return capture.SanitizedObservation{}, ErrRejected
	}
	// This redacted selector is used only for app/domain policy admission. The
	// document text goes through one retained streaming redactor, never through
	// separately redacted observations which could miss cross-batch secrets.
	selector := page
	selector.Text, selector.Complete, selector.Truncated = "Safari document capture", true, false
	selector.StructureLimited = false
	return Sanitize(selector, true, now)
}

func (r *Receiver) persistDocument(ctx context.Context, page Snapshot, selector capture.SanitizedObservation, at time.Time) (work documentWork, err error) {
	if ctx == nil {
		return work, ErrRejected
	}
	if err = ctx.Err(); err != nil {
		return work, err
	}
	r.mu.Lock()
	allowed := !r.paused && r.enabled()
	r.mu.Unlock()
	if !allowed {
		return work, ErrReceiverPaused
	}
	sink, ok := r.sink.(ingest.DocumentSink)
	if !ok {
		return work, ErrRejected
	}
	r.documentsMu.Lock()
	defer r.documentsMu.Unlock()
	if r.documents == nil {
		r.documents = make(map[string]*documentJob)
	}
	encoded, _ := json.Marshal(struct {
		DocumentID, Text   string
		Revision, Sequence int64
		Complete, Limited  bool
	}{page.DocumentID, page.Text, page.Revision, page.Sequence, page.Complete, page.StructureLimited})
	digest := sha256.Sum256(encoded)
	clear(encoded)
	job := r.documents[page.SourceKey]
	if job != nil && job.documentID == page.DocumentID && page.Revision < job.revision {
		return work, ErrDocumentRestart
	}
	if job == nil || job.documentID != page.DocumentID || job.revision != page.Revision {
		if page.Sequence != 0 {
			return work, ErrDocumentRestart
		}
		if job != nil {
			job.builder.Abort()
			work.cleanup = append(work.cleanup, job.builder.Header().CaptureID())
			delete(r.documents, page.SourceKey)
		}
		if len(r.documents) >= maxDocumentJobs {
			var oldest string
			for key, value := range r.documents {
				if oldest == "" || value.touched.Before(r.documents[oldest].touched) {
					oldest = key
				}
			}
			evicted := r.documents[oldest]
			evicted.builder.Abort()
			work.cleanup = append(work.cleanup, evicted.builder.Header().CaptureID())
			delete(r.documents, oldest)
		}
		builder, err := capture.NewCaptureBuilder(SafariBundleID, selector.Domain(), page.SourceKey, at)
		if err != nil {
			return work, err
		}
		if err := sink.BeginCapture(ctx, builder.Header()); err != nil {
			builder.Abort()
			return work, err
		}
		job = &documentJob{documentID: page.DocumentID, revision: page.Revision, builder: builder, title: page.Title, touched: at}
		r.documents[page.SourceKey] = job
	}
	if selector.Domain() != job.builder.Header().Domain() {
		return work, ErrDocumentRestart
	}
	job.touched = at
	if page.Sequence < job.next {
		if page.Sequence != job.next-1 || digest != job.last {
			return work, ErrDocumentRestart
		}
		if !page.Complete || job.finished {
			return work, nil
		}
	} else {
		if page.Sequence != job.next || job.finished {
			return work, ErrDocumentRestart
		}
		raw := page.Text
		if job.next == 0 && strings.TrimSpace(job.title) != "" {
			raw = strings.TrimSpace(job.title) + "\n" + raw
		}
		if err := job.builder.Write(ctx, raw, func(ctx context.Context, parts []capture.CapturePart) error {
			return sink.AppendCaptureParts(ctx, job.builder.Header(), parts)
		}); err != nil {
			return work, err
		}
		for _, character := range raw {
			if job.characters < capture.MinObservationChars && !unicode.IsSpace(character) {
				job.characters++
			}
		}
		job.next++
		job.last = digest
		job.limited = job.limited || page.StructureLimited
	}
	if !page.Complete {
		return work, nil
	}
	if job.characters < capture.MinObservationChars {
		r.droppedShort.Add(1)
		job.finished = true
		job.builder.Abort()
		work.cleanup = append(work.cleanup, job.builder.Header().CaptureID())
		return work, nil
	}
	if job.summary.ValidateForAdmission() != nil {
		coverage := capture.CaptureComplete
		if job.limited {
			coverage = capture.CaptureStructureLimited
		}
		summary, err := job.builder.Finish(ctx, coverage, func(ctx context.Context, parts []capture.CapturePart) error {
			return sink.AppendCaptureParts(ctx, job.builder.Header(), parts)
		})
		if err != nil {
			return work, err
		}
		job.summary = summary
	}
	receipt, err := sink.PublishDocumentCapture(ctx, job.summary)
	if !receipt.Committed {
		if err == nil {
			err = ErrRejected
		}
		return work, err
	}
	job.finished = true
	work.summary, work.committed, work.fresh = job.summary, true, receipt.New
	work.cleanup = append(work.cleanup, job.builder.Header().CaptureID())
	return work, err
}

func (r *Receiver) finishDocumentWork(work documentWork) {
	// Preference admission is released before cleanup and notifications.
	if work.committed {
		r.mu.Lock()
		notify := r.onDocument
		r.mu.Unlock()
		if notify != nil {
			notify(work.summary, work.fresh)
		}
	}
	for _, id := range work.cleanup {
		r.discardDocument(id)
	}
}

func (r *Receiver) discardDocument(id string) {
	if sink, ok := r.sink.(ingest.DocumentSink); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := sink.DiscardCaptureStage(ctx, id); err != nil {
			slog.Warn("Safari capture stage cleanup deferred", "result", "cleanup_pending")
		}
	}
}

func (r *Receiver) closeDocuments() {
	r.documentsMu.Lock()
	var ids []string
	for key, job := range r.documents {
		job.builder.Abort()
		ids = append(ids, job.builder.Header().CaptureID())
		delete(r.documents, key)
	}
	r.documentsMu.Unlock()
	for _, id := range ids {
		r.discardDocument(id)
	}
}
