package privacy

import (
	"context"
	"math"
	"slices"
	"sort"
	"strings"
)

// OriginSpan maps sanitized output bytes to their original logical input.
// Exact spans preserve bytes one to one; other spans describe an envelope.
// OpenEnded marks a forced mask whose known prefix is not its full extent.
type OriginSpan struct {
	OutputStart, OutputEnd int64
	InputStart, InputEnd   int64
	Exact, OpenEnded       bool
}

// MappedText contains sanitized text and global exclusive byte positions.
type MappedText struct {
	Text  string
	Spans []OriginSpan
}

// MappedStream is a single-owner stream with transactional source positions.
type MappedStream struct{ inner *Stream }
type streamLineage struct {
	spans []OriginSpan
	next  int64
}

func NewMappedStream() *MappedStream {
	s := NewStream()
	s.origins = make([]streamLineage, len(streamRules))
	return &MappedStream{inner: s}
}
func (s *MappedStream) Write(ctx context.Context, input string, sink func(context.Context, MappedText) error) error {
	return s.operation(ctx, input, false, sink)
}
func (s *MappedStream) Finish(ctx context.Context, sink func(context.Context, MappedText) error) error {
	return s.operation(ctx, "", true, sink)
}

// FinishWithCommit also invokes the sink for an empty final output, allowing
// callers to commit their own pending sanitized data in the EOF transaction.
func (s *MappedStream) FinishWithCommit(ctx context.Context, sink func(context.Context, MappedText) error) error {
	if s == nil || s.inner == nil {
		return ErrStreamClosed
	}
	if sink == nil {
		return s.inner.operationMapped(ctx, "", true, true, nil)
	}
	return s.inner.operationMapped(ctx, "", true, true, func(ctx context.Context, text string, spans []OriginSpan) error {
		return sink(ctx, MappedText{Text: text, Spans: spans})
	})
}
func (s *MappedStream) operation(ctx context.Context, input string, final bool, sink func(context.Context, MappedText) error) error {
	if s == nil || s.inner == nil {
		return ErrStreamClosed
	}
	if sink == nil {
		return s.inner.operationMapped(ctx, input, final, false, nil)
	}
	return s.inner.operationMapped(ctx, input, final, false, func(ctx context.Context, text string, spans []OriginSpan) error {
		return sink(ctx, MappedText{Text: text, Spans: spans})
	})
}
func (s *MappedStream) Abort() {
	if s != nil {
		s.inner.Abort()
	}
}
func (s *MappedStream) Counts() []Count {
	if s == nil {
		return nil
	}
	return s.inner.Counts()
}

func (l streamLineage) clone() streamLineage {
	return streamLineage{spans: slices.Clone(l.spans), next: l.next}
}

// A stage's output coordinates become the next stage's input coordinates.
func (l *streamLineage) append(length int, spans []OriginSpan) error {
	if l.next < 0 || int64(length) > math.MaxInt64-l.next {
		return ErrStreamPositionOverflow
	}
	position := int64(0)
	for _, span := range spans {
		if span.OutputStart != position || span.OutputEnd <= position || span.OutputEnd > int64(length) {
			return errStreamMatch
		}
		position = span.OutputEnd
		span.OutputStart += l.next
		span.OutputEnd += l.next
		l.spans = appendOrigin(l.spans, span)
	}
	if position != int64(length) {
		return errStreamMatch
	}
	l.next += int64(length)
	return nil
}

// slice preserves an exact run's byte translation and an opaque run's envelope.
func (l *streamLineage) slice(start, end int64) ([]OriginSpan, error) {
	if start < 0 || end < start || end > l.next {
		return nil, errStreamMatch
	}
	var result []OriginSpan
	position := start
	// Stage coordinates are ordered. Skip consumed entries without rescanning
	// the entire retained prefix for every match in a dense input batch.
	first := sort.Search(len(l.spans), func(i int) bool { return l.spans[i].OutputEnd > start })
	for _, span := range l.spans[first:] {
		if span.OutputStart >= end {
			break
		}
		lo, hi := max(start, span.OutputStart), min(end, span.OutputEnd)
		if lo != position {
			return nil, errStreamMatch
		}
		if span.Exact {
			span.InputStart += lo - span.OutputStart
			span.InputEnd = span.InputStart + hi - lo
		}
		span.OutputStart, span.OutputEnd = lo-start, hi-start
		result = appendOrigin(result, span)
		position = hi
	}
	if position != end {
		return nil, errStreamMatch
	}
	return result, nil
}

func (l *streamLineage) retire(before int64) {
	n := 0
	for n < len(l.spans) && l.spans[n].OutputEnd <= before {
		n++
	}
	if n > 0 {
		left := copy(l.spans, l.spans[n:])
		clear(l.spans[left:])
		l.spans = l.spans[:left]
	}
	if len(l.spans) > 0 && l.spans[0].OutputStart < before {
		span := &l.spans[0]
		if span.Exact {
			span.InputStart += before - span.OutputStart
		}
		span.OutputStart = before
	}
}

func appendOrigin(spans []OriginSpan, span OriginSpan) []OriginSpan {
	if span.OutputStart == span.OutputEnd {
		return spans
	}
	if len(spans) > 0 {
		last := &spans[len(spans)-1]
		if last.OutputEnd == span.OutputStart && last.Exact == span.Exact && last.OpenEnded == span.OpenEnded &&
			(last.Exact && last.InputEnd == span.InputStart || !last.Exact && last.InputStart == span.InputStart && last.InputEnd == span.InputEnd) {
			last.OutputEnd = span.OutputEnd
			if last.Exact {
				last.InputEnd = span.InputEnd
			}
			return spans
		}
	}
	return append(spans, span)
}

func transformedOrigin(spans []OriginSpan, length int, forced bool) []OriginSpan {
	if length == 0 {
		return nil
	}
	span := OriginSpan{OutputEnd: int64(length), InputStart: math.MaxInt64, OpenEnded: forced}
	for _, source := range spans {
		span.InputStart = min(span.InputStart, source.InputStart)
		span.InputEnd = max(span.InputEnd, source.InputEnd)
		span.OpenEnded = span.OpenEnded || source.OpenEnded
	}
	return []OriginSpan{span}
}

func (s *Stream) processMapped(ctx context.Context, input string, final bool) (string, []OriginSpan, map[string]int, error) {
	if len(s.origins) != len(s.stages) {
		return "", nil, nil, errStreamMatch
	}
	var output strings.Builder
	var spans []OriginSpan
	delta := map[string]int{}
	var send func(int, string, []OriginSpan) error
	feed := func(i int, text string, end bool) error {
		err := s.stages[i].feedSpans(ctx, text, end, func(plain string, start int64) error {
			if plain == "" {
				return nil
			}
			origins, err := s.origins[i].slice(start, start+int64(len(plain)))
			if err != nil {
				return err
			}
			return send(i+1, plain, origins)
		}, func(raw string, forced bool, start int64) error {
			origins, err := s.origins[i].slice(start, start+int64(len(raw)))
			if err != nil {
				return err
			}
			replacement := streamReplacement(streamRules[i], raw, forced, delta)
			if replacement != raw || forced {
				origins = transformedOrigin(origins, len(replacement), forced)
			}
			return send(i+1, replacement, origins)
		})
		before := s.stages[i].start
		if s.stages[i].drain != 0 {
			before = s.stages[i].pos
		}
		s.origins[i].retire(before)
		return err
	}
	send = func(i int, text string, origins []OriginSpan) error {
		if text == "" {
			return nil
		}
		if i == len(s.stages) {
			if s.outputPosition < 0 || int64(output.Len()) > math.MaxInt64-s.outputPosition || int64(len(text)) > math.MaxInt64-s.outputPosition-int64(output.Len()) {
				return ErrStreamPositionOverflow
			}
			offset := s.outputPosition + int64(output.Len())
			for _, span := range origins {
				span.OutputStart += offset
				span.OutputEnd += offset
				spans = appendOrigin(spans, span)
			}
			output.WriteString(text)
			return nil
		}
		if err := s.origins[i].append(len(text), origins); err != nil {
			return err
		}
		return feed(i, text, false)
	}
	if final {
		for i := range s.stages {
			if err := feed(i, "", true); err != nil {
				return "", nil, nil, err
			}
		}
	} else if input != "" {
		start := s.stages[0].pos
		if start < 0 || int64(len(input)) > math.MaxInt64-start {
			return "", nil, nil, ErrStreamPositionOverflow
		}
		origins := []OriginSpan{{OutputEnd: int64(len(input)), InputStart: start, InputEnd: start + int64(len(input)), Exact: true}}
		if err := send(0, input, origins); err != nil {
			return "", nil, nil, err
		}
	}
	s.outputPosition += int64(output.Len())
	return output.String(), spans, delta, nil
}
