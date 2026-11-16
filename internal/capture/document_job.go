package capture

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/platform"
)

// ErrDocumentUnsupported permits the ordinary bounded/visual capture fallback.
// Unsupported adapters return it before probing, admitting, or reading content.
var ErrDocumentUnsupported = errors.New("capture: document streaming unsupported")

type DocumentJob interface {
	Header() CaptureHeader
	Read(context.Context, func(context.Context, []CapturePart) error) (DocumentProgress, error)
	Close()
}
type DocumentProgress struct {
	Done             bool
	Summary          CaptureSummary
	Bytes, Nodes     int
	StructureLimited bool
}

func (p Pipeline) OpenDocument(ctx context.Context, at time.Time) (DocumentJob, error) {
	if !supportsDocumentSource(p.Source) {
		return nil, ErrDocumentUnsupported
	}
	if ctx == nil || !p.Limits.valid() {
		return nil, captureError(InvalidLimits, nil)
	}
	exclusions, ok := canonicalExclusions(p.Exclusions)
	if !ok {
		return nil, captureError(InvalidExclusions, nil)
	}
	list, ok := profilesWithBuiltins(p.Profiles)
	if !ok {
		return nil, captureError(InvalidProfiles, nil)
	}
	profiles, ok := validateProfiles(list)
	if !ok {
		return nil, captureError(InvalidProfiles, nil)
	}
	round, cancel := context.WithTimeout(ctx, p.Limits.MaxDuration)
	defer cancel()
	if err := round.Err(); err != nil {
		return nil, contextError(err)
	}
	report, err := p.Source.Capability(round)
	if err != nil || report.Validate() != nil || report.ID != "capture" || report.State != platform.Available {
		if ce := contextError(round.Err()); ce != nil {
			return nil, ce
		}
		if report.State == platform.PermissionRequired {
			return nil, captureError(PermissionRequired, nil)
		}
		return nil, captureError(Unavailable, nil)
	}
	if presence, ok := p.Source.(platform.CapturePresence); ok {
		idle, err := presence.UserIdle(round)
		if err != nil {
			return nil, captureError(Unavailable, err)
		}
		if idle {
			return nil, captureError(UserIdle, nil)
		}
	}
	probe, err := p.Source.Probe(round)
	if err != nil {
		return nil, captureError(ProbeFailed, err)
	}
	app, domain, ok := normalizeProbe(probe)
	if !ok {
		return nil, captureError(InvalidProbe, nil)
	}
	if p.CurrentExclusions != nil {
		current, err := p.CurrentExclusions(round)
		if err != nil {
			return nil, captureError(Excluded, err)
		}
		exclusions, ok = canonicalExclusions(current)
		if !ok {
			return nil, captureError(InvalidExclusions, nil)
		}
	}
	mayDiscover := appidentity.IsBrowserApp(app) && domain == "" && len(exclusions.Domains)+len(exclusions.IncludedDomains)+len(exclusions.RequiredDomains) > 0
	if probe.Secure || probe.Elevated || probe.Private || excluded(app, domain, exclusions) || !included(app, domain, exclusions) && !mayDiscover {
		return nil, captureError(Excluded, nil)
	}
	ignored := func(role string) bool { return ignoredAXRole(app, role, profiles) }
	return openPlatformDocument(round, p, probe, app, domain, exclusions, at, ignored)
}

type axCaptureDocumentJob struct {
	cursor        *axDocumentCursor
	builder       *CaptureBuilder
	limits        Limits
	guard         func(context.Context) error
	release       func()
	wrote, closed bool
	meaningful    bool
	noProgress    int
	summary       CaptureSummary
}

func (j *axCaptureDocumentJob) Header() CaptureHeader {
	if j == nil || j.builder == nil {
		return CaptureHeader{}
	}
	return j.builder.Header()
}
func (j *axCaptureDocumentJob) Close() {
	if j == nil || j.closed {
		return
	}
	j.closed = true
	if j.cursor != nil {
		j.cursor.close()
	}
	if j.builder != nil {
		j.builder.Abort()
	}
	if j.release != nil {
		j.release()
		j.release = nil
	}
}
func (j *axCaptureDocumentJob) Read(ctx context.Context, sink func(context.Context, []CapturePart) error) (DocumentProgress, error) {
	var out DocumentProgress
	if j == nil || j.closed || ctx == nil || sink == nil {
		return out, ErrCaptureClosed
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	// One absolute deadline covers target validation, cursor work, final part
	// persistence and the post-Finish validation. Keep the original owner
	// context separate so a round deadline never closes the retained cursor.
	round, cancel := context.WithTimeout(ctx, j.limits.MaxDuration)
	defer cancel()
	if err := j.guard(round); err != nil {
		return j.roundFailure(ctx, round, out, err, false)
	}
	if err := round.Err(); err != nil {
		return j.roundFailure(ctx, round, out, err, false)
	}
	if j.summary.Validate() == nil {
		j.noProgress = 0
		return DocumentProgress{Done: true, Summary: j.summary}, nil
	}
	progress, err := j.cursor.readInRound(ctx, round, j.limits, func(round context.Context, piece string, first bool) error {
		// Reserve one byte in the cursor's range for this separator. One Write is
		// transactional: a failed sink cannot consume a separator on its own.
		raw := piece
		if first && j.wrote {
			raw = "\n" + raw
		}
		if err := j.builder.Write(round, raw, sink); err != nil {
			return err
		}
		j.wrote = true
		j.meaningful = j.meaningful || strings.TrimSpace(piece) != ""
		return nil
	})
	out.Bytes, out.Nodes, out.StructureLimited = progress.Bytes, progress.Nodes, progress.StructureLimited
	if err != nil {
		return j.roundFailure(ctx, round, out, err, progress.Advanced)
	}
	if !progress.Done {
		if !progress.Advanced {
			j.noProgress++
		} else {
			j.noProgress = 0
		}
		if j.noProgress >= 3 {
			return out, captureError(TimedOut, nil)
		}
		return out, nil
	}
	if !j.meaningful {
		return out, captureError(NoContent, nil)
	}
	if err := j.guard(round); err != nil {
		return j.roundFailure(ctx, round, out, err, progress.Advanced)
	}
	if err := round.Err(); err != nil {
		return j.roundFailure(ctx, round, out, err, progress.Advanced)
	}
	coverage := CaptureComplete
	if progress.StructureLimited {
		coverage = CaptureStructureLimited
	}
	summary, err := j.builder.Finish(round, coverage, sink)
	if err != nil {
		return j.roundFailure(ctx, round, out, err, progress.Advanced)
	}
	// Finish closes the builder. Retain its checked result privately even if
	// final authority validation exhausts this round; a fresh Read must validate
	// again before returning it, and must never call Finish twice.
	j.summary = summary
	if err := j.guard(round); err != nil {
		return j.roundFailure(ctx, round, out, err, true)
	}
	if err := round.Err(); err != nil {
		return j.roundFailure(ctx, round, out, err, true)
	}
	j.noProgress = 0
	out.Done, out.Summary = true, summary
	return out, nil
}

func (j *axCaptureDocumentJob) roundFailure(owner, round context.Context, out DocumentProgress, err error, advanced bool) (DocumentProgress, error) {
	if cause := owner.Err(); cause != nil {
		return out, cause
	}
	if errors.Is(err, context.DeadlineExceeded) && round.Err() != nil {
		if advanced {
			j.noProgress = 0
		} else {
			j.noProgress++
		}
		if j.noProgress >= 3 {
			return out, captureError(TimedOut, nil)
		}
		return out, nil
	}
	if ce := contextError(err); ce != nil {
		return out, ce
	}
	var ce *CaptureError
	if errors.As(err, &ce) {
		return out, err
	}
	return out, captureError(ReadFailed, err)
}
