package capture

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/privacy"
)

type captureBuildState struct {
	rawBytes, index, bytes int64
	root                   string
	pending                []byte
	origins                []privacy.OriginSpan
}

func (s captureBuildState) clone() captureBuildState {
	s.pending = slices.Clone(s.pending)
	s.origins = slices.Clone(s.origins)
	return s
}
func (s *captureBuildState) clear() { clear(s.pending); *s = captureBuildState{} }

// CaptureBuilder is owned by one capture job. Only complete sanitized parts
// leave the builder; the sink stages them privately until a checked summary.
type CaptureBuilder struct {
	header                       CaptureHeader
	stream                       *privacy.MappedStream
	state                        captureBuildState
	closed, busy, abortRequested bool
}

func NewCaptureBuilder(appID, domain, sourceKey string, at time.Time) (*CaptureBuilder, error) {
	if len(appID) > 128 || len(domain) > 253 {
		return nil, ErrCaptureInvalid
	}
	app, ok := normalizeAppID(appID)
	host, hostOK := normalizeDomain(domain)
	if !ok || !hostOK || !captureHashID(sourceKey) || at.IsZero() || !time.Unix(0, at.UnixNano()).UTC().Equal(at) {
		return nil, ErrCaptureInvalid
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, ErrCaptureInvalid
	}
	h := CaptureHeader{appID: app, domain: host, sourceKey: sourceKey, familyID: captureFamilyID(app, host, sourceKey), captureID: hex.EncodeToString(nonce[:]), at: at.UTC(), admission: true, redactionVersion: privacy.StreamVersion}
	if h.ValidateForAdmission() != nil {
		return nil, ErrCaptureInvalid
	}
	return &CaptureBuilder{header: h, stream: privacy.NewMappedStream(), state: captureBuildState{root: h.InitialRoot()}}, nil
}
func (b *CaptureBuilder) Header() CaptureHeader {
	if b == nil {
		return CaptureHeader{}
	}
	return b.header
}
func (b *CaptureBuilder) Abort() {
	if b == nil {
		return
	}
	b.closed = true
	b.stream.Abort()
	if b.busy {
		b.abortRequested = true
		return
	}
	b.state.clear()
}
func (b *CaptureBuilder) Write(ctx context.Context, raw string, sink func(context.Context, []CapturePart) error) error {
	_, err := b.operation(ctx, raw, 0, false, sink)
	return err
}
func (b *CaptureBuilder) Finish(ctx context.Context, coverage CaptureCoverage, sink func(context.Context, []CapturePart) error) (CaptureSummary, error) {
	return b.operation(ctx, "", coverage, true, sink)
}
func (b *CaptureBuilder) operation(ctx context.Context, raw string, coverage CaptureCoverage, final bool, sink func(context.Context, []CapturePart) error) (CaptureSummary, error) {
	var empty CaptureSummary
	if b == nil {
		return empty, ErrCaptureClosed
	}
	if b.busy {
		return empty, ErrCaptureBusy
	}
	if b.closed || b.stream == nil {
		return empty, ErrCaptureClosed
	}
	if ctx == nil || sink == nil || final && (coverage != CaptureComplete && coverage != CaptureStructureLimited) || b.state.rawBytes > math.MaxInt64-int64(len(raw)) {
		return empty, ErrCaptureInvalid
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if final && b.state.rawBytes == 0 {
		return empty, ErrCaptureInvalid
	}
	b.busy = true
	b.abortRequested = false
	defer func() {
		b.busy = false
		if b.abortRequested {
			b.state.clear()
			b.closed = true
		}
	}()
	trial := b.state.clone()
	defer trial.clear()
	trial.rawBytes += int64(len(raw))
	commit := func(ctx context.Context, batch privacy.MappedText) error {
		parts, err := trial.appendMapped(b.header.captureID, batch, final)
		if err != nil {
			return err
		}
		if len(parts) > 0 {
			return sink(ctx, parts)
		}
		return nil
	}
	var err error
	if final {
		err = b.stream.FinishWithCommit(ctx, commit)
	} else {
		err = b.stream.Write(ctx, raw, commit)
	}
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return empty, context.Canceled
		case errors.Is(err, context.DeadlineExceeded):
			return empty, context.DeadlineExceeded
		case errors.Is(err, privacy.ErrStreamOutput):
			return empty, ErrCaptureOutput
		default:
			return empty, ErrCaptureInvalid
		}
	}
	b.state.clear()
	b.state = trial
	trial = captureBuildState{}
	if !final {
		return empty, nil
	}
	b.closed = true
	if b.abortRequested {
		return empty, ErrCaptureClosed
	}
	s := CaptureSummary{header: b.header, root: b.state.root, parts: b.state.index, bytes: b.state.bytes, rawBytes: b.state.rawBytes, coverage: coverage, counts: copyCounts(b.stream.Counts()), admission: true}
	s.contentID = captureSummaryID(s)
	if s.ValidateForAdmission() != nil {
		return empty, ErrCaptureInvalid
	}
	return s, nil
}

func captureOriginsMerge(a, b privacy.OriginSpan) bool {
	return a.OutputEnd == b.OutputStart && a.Exact == b.Exact && a.OpenEnded == b.OpenEnded && (a.Exact && a.InputEnd == b.InputStart || !a.Exact && a.InputStart == b.InputStart && a.InputEnd == b.InputEnd)
}
func captureOriginSlice(span privacy.OriginSpan, start, end int64) privacy.OriginSpan {
	if span.Exact {
		span.InputStart += start - span.OutputStart
		span.InputEnd = span.InputStart + end - start
	}
	span.OutputStart, span.OutputEnd = start, end
	return span
}
func (s *captureBuildState) flush(captureID string) (CapturePart, error) {
	if len(s.pending) == 0 || s.index == math.MaxInt64 || s.bytes > math.MaxInt64-int64(len(s.pending)) {
		return CapturePart{}, ErrCaptureInvalid
	}
	p := CapturePart{captureID: captureID, index: s.index, offset: s.bytes, text: string(s.pending), origins: slices.Clone(s.origins), admission: true}
	p.digest = capturePartDigest(p)
	root, err := AdvanceCaptureRoot(s.root, p)
	if err != nil {
		return CapturePart{}, err
	}
	s.root = root
	s.bytes += int64(len(s.pending))
	s.index++
	clear(s.pending)
	s.pending = s.pending[:0]
	clear(s.origins)
	s.origins = s.origins[:0]
	return p, nil
}
func (s *captureBuildState) appendMapped(captureID string, batch privacy.MappedText, final bool) ([]CapturePart, error) {
	var parts []CapturePart
	flush := func() error {
		p, err := s.flush(captureID)
		if err == nil {
			parts = append(parts, p)
		}
		return err
	}
	start := s.bytes + int64(len(s.pending))
	position := start
	if int64(len(batch.Text)) > math.MaxInt64-start {
		return nil, ErrCaptureInvalid
	}
	for _, span := range batch.Spans {
		if span.OutputStart != position || span.OutputEnd <= position || span.OutputEnd > start+int64(len(batch.Text)) || span.InputStart < 0 || span.InputEnd <= span.InputStart || span.InputEnd > s.rawBytes {
			return nil, ErrCaptureInvalid
		}
		for position < span.OutputEnd {
			remaining := captureOriginSlice(span, position, span.OutputEnd)
			if len(s.origins) == MaxCapturePartOrigins && !captureOriginsMerge(s.origins[len(s.origins)-1], remaining) {
				if err := flush(); err != nil {
					return nil, err
				}
				continue
			}
			n := min(int(span.OutputEnd-position), MaxCapturePartTextBytes-len(s.pending))
			for n > 0 && position+int64(n) < start+int64(len(batch.Text)) && !utf8.RuneStart(batch.Text[position-start+int64(n)]) {
				n--
			}
			if n == 0 {
				if err := flush(); err != nil {
					return nil, err
				}
				continue
			}
			fragment := captureOriginSlice(span, position, position+int64(n))
			if len(s.origins) > 0 && captureOriginsMerge(s.origins[len(s.origins)-1], fragment) {
				last := &s.origins[len(s.origins)-1]
				last.OutputEnd = fragment.OutputEnd
				if last.Exact {
					last.InputEnd = fragment.InputEnd
				}
			} else {
				s.origins = append(s.origins, fragment)
			}
			s.pending = append(s.pending, batch.Text[position-start:position-start+int64(n)]...)
			position += int64(n)
			if len(s.pending) == MaxCapturePartTextBytes {
				if err := flush(); err != nil {
					return nil, err
				}
			}
		}
	}
	if position != start+int64(len(batch.Text)) {
		return nil, ErrCaptureInvalid
	}
	if final && len(s.pending) > 0 {
		if err := flush(); err != nil {
			return nil, err
		}
	}
	return parts, nil
}
