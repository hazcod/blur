package privacy

import (
	"context"
	"errors"
	"math"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestStreamMatcherOriginCoordinatesEverySplit(t *testing.T) {
	cases := []struct {
		re *regexp.Regexp
		in string
	}{
		{quotedLabel, "日本語🙂 password:\"private🙂\" ordinary end"},
		{webURL, "before https://example.test/path?token=secret 🙂 tail"},
		{pem, "before -----BEGIN PRIVATE KEY-----secret-----END PRIVATE KEY----- after"},
		{token, "before xoxb-aaaaaaaaaaaaaaaaaaaa----_ ordinary ghp_bbbbbbbbbbbbbbbbbbbbbbbbb tail"},
	}
	for _, tc := range cases {
		for cut := 0; cut <= len(tc.in); cut++ {
			if !utf8.ValidString(tc.in[:cut]) {
				continue
			}
			m := newStreamMatcher(newStreamPattern(tc.re, tc.re == pem, nil))
			var got strings.Builder
			var locations [][2]int
			plain := func(text string, start int64) error {
				if len(text) > streamForwardBytes {
					t.Fatal("unbounded plain callback")
				}
				if start != int64(got.Len()) || tc.in[start:start+int64(len(text))] != text {
					t.Fatal("incorrect original plaintext coordinates", cut, start)
				}
				got.WriteString(text)
				return nil
			}
			match := func(text string, forced bool, start int64) error {
				if forced || start != int64(got.Len()) || tc.in[start:start+int64(len(text))] != text {
					t.Fatal("incorrect original match coordinates", cut, start, forced)
				}
				locations = append(locations, [2]int{int(start), int(start) + len(text)})
				got.WriteString(text)
				return nil
			}
			for _, piece := range []string{tc.in[:cut], tc.in[cut:]} {
				if err := m.feedSpans(context.Background(), piece, false, plain, match); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.feedSpans(context.Background(), "", true, plain, match); err != nil {
				t.Fatal(err)
			}
			var want [][2]int
			for _, loc := range tc.re.FindAllStringIndex(tc.in, -1) {
				want = append(want, [2]int{loc[0], loc[1]})
			}
			if got.String() != tc.in || !slices.Equal(locations, want) {
				t.Fatal("original coordinates differ from independent match extents", cut, locations, want)
			}
		}
	}
}

func TestStreamMatcherOriginAfterOversizedDrain(t *testing.T) {
	input := "🙂 before password:\"" + strings.Repeat("x", 3*streamPendingBytes) + "\" original tail"
	m := newStreamMatcher(newStreamPattern(quotedLabel, false, nil))
	forced := 0
	tailStart := int64(-1)
	plain := func(text string, start int64) error {
		if text != input[start:start+int64(len(text))] {
			t.Fatal("plain origin moved during drain")
		}
		if at := strings.Index(text, "original tail"); at >= 0 {
			tailStart = start + int64(at)
		}
		return nil
	}
	match := func(text string, force bool, start int64) error {
		if !force || start != int64(strings.Index(input, "password")) || text != input[start:start+int64(len(text))] {
			t.Fatal("wrong forced prefix origin")
		}
		forced++
		return nil
	}
	for _, piece := range streamPieces(input, 4096) {
		if err := m.feedSpans(context.Background(), piece, false, plain, match); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.feedSpans(context.Background(), "", true, plain, match); err != nil {
		t.Fatal(err)
	}
	if forced != 1 || tailStart != int64(strings.Index(input, "original tail")) {
		t.Fatal("drained bytes changed later source coordinates", forced, tailStart)
	}
}

func TestStreamMatcherOriginRejectsPositionOverflow(t *testing.T) {
	m := newStreamMatcher(newStreamPattern(webURL, false, nil))
	m.resetAt(math.MaxInt64-1, 'x')
	emitted := false
	err := m.feedSpans(context.Background(), "xx", false, func(string, int64) error { emitted = true; return nil }, func(string, bool, int64) error { emitted = true; return nil })
	if !errors.Is(err, ErrStreamPositionOverflow) || emitted || m.pos != math.MaxInt64-1 {
		t.Fatal("position overflow was consumed", err, emitted, m.pos)
	}
}

func collectMapped(t *testing.T, input string, pieces []string) (string, []OriginSpan, []Count) {
	t.Helper()
	s := NewMappedStream()
	var output strings.Builder
	var spans []OriginSpan
	sink := func(_ context.Context, batch MappedText) error {
		t.Helper()
		checkMappedBatch(t, input, int64(output.Len()), batch)
		output.WriteString(batch.Text)
		spans = append(spans, batch.Spans...)
		return nil
	}
	for _, piece := range pieces {
		if err := s.Write(context.Background(), piece, sink); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Finish(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	return output.String(), spans, s.Counts()
}
func checkMappedBatch(t *testing.T, input string, offset int64, batch MappedText) {
	t.Helper()
	position := offset
	for _, span := range batch.Spans {
		if span.OutputStart != position || span.OutputEnd <= position || span.OutputEnd > offset+int64(len(batch.Text)) || span.InputStart < 0 || span.InputEnd <= span.InputStart || span.InputEnd > int64(len(input)) {
			t.Fatal("invalid source partition", span, offset, len(batch.Text))
		}
		if !utf8.ValidString(input[:span.InputStart]) || !utf8.ValidString(input[:span.InputEnd]) || !utf8.ValidString(batch.Text[:span.OutputEnd-offset]) {
			t.Fatal("origin split a UTF-8 scalar", span)
		}
		if span.Exact && (span.OpenEnded || input[span.InputStart:span.InputEnd] != batch.Text[span.OutputStart-offset:span.OutputEnd-offset]) {
			t.Fatal("exact origin does not preserve original bytes", span)
		}
		position = span.OutputEnd
	}
	if batch.Text == "" || position != offset+int64(len(batch.Text)) {
		t.Fatal("source map does not cover emitted text", position, offset, len(batch.Text))
	}
}

func TestMappedStreamEverySplitPreservesTextCountsAndOrigins(t *testing.T) {
	cases := []string{
		"日本語🙂 ordinary tail",
		"before password:\"private🙂\" after",
		"before https://example.test/path?token=secret&view=day after",
		"before password:\"a -----BEGIN PRIVATE KEY-----\nprivate\n-----END PRIVATE KEY----- b\" after",
		"before Authorization: Bearer private\nafter",
		"before xoxb-aaaaaaaaaaaaaaaaaaaa----_ ghp_bbbbbbbbbbbbbbbbbbbbbbbbb after",
		"xſecret:\"private\" ordinary",
		"password:\"secret https://example.test/path unfinished",
		"4111111111111112 https://example.test/ordinary tail",
		"before 123-45-6789 after GB82 WEST 1234 5698 7654 32 end",
	}
	for _, input := range cases {
		want, counts := streamResult(t, []string{input})
		for cut := 0; cut <= len(input); cut++ {
			if !utf8.ValidString(input[:cut]) {
				continue
			}
			got, _, gotCounts := collectMapped(t, input, []string{input[:cut], input[cut:]})
			if got != want || !slices.Equal(counts, gotCounts) {
				t.Fatal("mapping changed redaction behavior", cut, got, gotCounts)
			}
		}
	}
}

func TestMappedStreamNestedReplacementEnvelopeAndOrdinaryTail(t *testing.T) {
	input := "🙂 prefix password:\"a -----BEGIN PRIVATE KEY-----\nprivate\n-----END PRIVATE KEY----- b\" ordinary tail"
	output, spans, _ := collectMapped(t, input, streamPieces(input, 7))
	secretAt := int64(strings.Index(output, "[REDACTED:SECRET]"))
	found := false
	for _, span := range spans {
		if span.OutputStart <= secretAt && secretAt < span.OutputEnd {
			found = true
			if span.Exact || span.OpenEnded || span.InputStart != int64(strings.Index(input, "password")) || span.InputEnd != int64(strings.Index(input, " ordinary tail")) {
				t.Fatal("nested replacement lost contributing source envelope", span)
			}
		}
	}
	if !found {
		t.Fatal("no origin for nested secret")
	}
}

func TestMappedStreamNewlineRemovalExtendsLaterUnquotedSourceEnvelope(t *testing.T) {
	input := "🙂 prefix password: a -----BEGIN PRIVATE KEY-----\nhidden\n-----END PRIVATE KEY----- suffix\nordinary tail"
	for _, size := range []int{1, 7, MaxStreamInputBytes} {
		output, spans, _ := collectMapped(t, input, streamPieces(input, size))
		if output != "🙂 prefix password: [REDACTED:SECRET]\nordinary tail" {
			t.Fatal("newline-removing replacement did not feed later unquoted rule", output)
		}
		at := int64(strings.Index(output, "[REDACTED:SECRET]"))
		found := false
		for _, span := range spans {
			if span.OutputStart <= at && at < span.OutputEnd {
				found = true
				if span.Exact || span.OpenEnded || span.InputStart != int64(strings.Index(input, "password")) || span.InputEnd != int64(strings.Index(input, "\nordinary tail")) {
					t.Fatal("later match lost removed-newline source envelope", span)
				}
			}
		}
		if !found {
			t.Fatal("no origin for replacement-created match")
		}
	}
}

func BenchmarkStreamLineageDenseRangeLookup(b *testing.B) {
	lineage := streamLineage{}
	const entries = 4096
	for i := int64(0); i < entries; i++ {
		lineage.spans = append(lineage.spans, OriginSpan{OutputStart: i, OutputEnd: i + 1, InputStart: i * 2, InputEnd: i*2 + 1, Exact: i%2 == 0})
	}
	lineage.next = entries
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		for i := int64(0); i < entries; i++ {
			if _, err := lineage.slice(i, i+1); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func TestMappedStreamLargeOrdinaryDocumentAndOversizedDrainStayBounded(t *testing.T) {
	input := strings.Repeat("Build notes 日本語🙂.\n", 60000) + "tail fact: rollback owner is Mira."
	got, _, counts := collectMapped(t, input, streamPieces(input, MaxStreamInputBytes))
	if got != input || len(counts) != 0 {
		t.Fatal("large ordinary document lost its tail")
	}
	input = "before password:\"" + strings.Repeat("x", 2<<20) + "\" original tail"
	s := NewMappedStream()
	var output strings.Builder
	var spans []OriginSpan
	sink := func(_ context.Context, batch MappedText) error {
		checkMappedBatch(t, input, int64(output.Len()), batch)
		output.WriteString(batch.Text)
		spans = append(spans, batch.Spans...)
		return nil
	}
	for _, piece := range streamPieces(input, 4096) {
		if err := s.Write(context.Background(), piece, sink); err != nil {
			t.Fatal(err)
		}
		for _, lineage := range s.inner.origins {
			if len(lineage.spans) > 2*streamPendingBytes {
				t.Fatal("draining field retained unbounded origin metadata", len(lineage.spans))
			}
			covered := int64(0)
			for _, span := range lineage.spans {
				covered += span.OutputEnd - span.OutputStart
			}
			if covered > streamPendingBytes {
				t.Fatal("origin queue retained consumed drain bytes", covered)
			}
		}
	}
	if err := s.Finish(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if output.String() != "before [REDACTED:SECRET] original tail" {
		t.Fatal("oversized mapped field changed output", output.String())
	}
	opaque, tail := false, false
	tailOffset := int64(strings.Index(output.String(), "original tail"))
	for _, span := range spans {
		opaque = opaque || span.OpenEnded
		if span.Exact && span.OutputStart <= tailOffset && tailOffset < span.OutputEnd {
			tail = span.InputStart+tailOffset-span.OutputStart == int64(strings.Index(input, "original tail"))
		}
	}
	if !opaque || !tail {
		t.Fatal("oversized field fabricated extent or moved original tail", opaque, tail)
	}
}

func TestMappedStreamSinkFailureRollsBackPendingTextCountsAndPositions(t *testing.T) {
	input := "prefix password:\"private\" ordinary tail"
	s := NewMappedStream()
	var out strings.Builder
	var spans []OriginSpan
	sink := func(_ context.Context, batch MappedText) error {
		checkMappedBatch(t, input, int64(out.Len()), batch)
		out.WriteString(batch.Text)
		spans = append(spans, batch.Spans...)
		return nil
	}
	if err := s.Write(context.Background(), "prefix password:\"pri", sink); err != nil {
		t.Fatal(err)
	}
	before := out.String()
	var rejected MappedText
	err := s.Write(context.Background(), "vate\" ordinary tail", func(_ context.Context, b MappedText) error {
		rejected = MappedText{b.Text, slices.Clone(b.Spans)}
		return errors.New("private underlying failure")
	})
	if !errors.Is(err, ErrStreamOutput) || s.inner.outputPosition != int64(len(before)) || len(s.Counts()) != 0 {
		t.Fatal("rejected mapped write committed", err, s.Counts())
	}
	err = s.Write(context.Background(), "vate\" ordinary tail", func(ctx context.Context, b MappedText) error {
		if b.Text != rejected.Text || !slices.Equal(b.Spans, rejected.Spans) {
			t.Fatal("retry changed source positions")
		}
		return sink(ctx, b)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	want, counts := streamResult(t, []string{input})
	if out.String() != want || !slices.Equal(s.Counts(), counts) {
		t.Fatal("mapped retry omitted or duplicated output")
	}
}

func TestMappedStreamFinishRetryAbortReentryAndCallerMutation(t *testing.T) {
	ctx := context.Background()
	s := NewMappedStream()
	input := "password:\"unfinished"
	if err := s.Write(ctx, input, func(context.Context, MappedText) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var failed MappedText
	if err := s.Finish(ctx, func(_ context.Context, b MappedText) error {
		failed = MappedText{b.Text, slices.Clone(b.Spans)}
		return context.DeadlineExceeded
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lost typed deadline", err)
	}
	if err := s.Finish(ctx, func(_ context.Context, b MappedText) error {
		if b.Text != failed.Text || !slices.Equal(b.Spans, failed.Spans) {
			t.Fatal("Finish retry changed origin")
		}
		if err := s.Write(ctx, "reenter", func(context.Context, MappedText) error { return nil }); !errors.Is(err, ErrStreamBusy) {
			t.Fatal("mapped reentry admitted", err)
		}
		s.Abort()
		clear(b.Spans)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(s.Counts()) != 1 {
		t.Fatal("Abort after accepted sink lost counts")
	}
	if err := s.Finish(ctx, func(context.Context, MappedText) error { return nil }); !errors.Is(err, ErrStreamClosed) {
		t.Fatal("callback Abort resurrected mapped stream", err)
	}
	s = NewMappedStream()
	if err := s.Write(ctx, "before password:\"sec", func(_ context.Context, b MappedText) error { clear(b.Spans); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, "ret\" tail", func(_ context.Context, b MappedText) error {
		checkMappedBatch(t, "before password:\"secret\" tail", 7, b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, func(context.Context, MappedText) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestMappedStreamRejectsInvalidInputAndPositionOverflow(t *testing.T) {
	ctx := context.Background()
	sink := func(context.Context, MappedText) error { return nil }
	var absent *MappedStream
	if err := absent.Write(ctx, "text", sink); !errors.Is(err, ErrStreamClosed) {
		t.Fatal(err)
	}
	absent.Abort()
	if len(absent.Counts()) != 0 {
		t.Fatal("nil counts")
	}
	s := NewMappedStream()
	for _, input := range []string{"\x00", "\xff", strings.Repeat("x", MaxStreamInputBytes+1)} {
		if err := s.Write(ctx, input, sink); !errors.Is(err, ErrStreamInput) {
			t.Fatal("invalid mapped input accepted", err)
		}
	}
	if err := s.Write(ctx, "text", nil); !errors.Is(err, ErrStreamInput) {
		t.Fatal("nil mapped sink accepted", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Write(cancelled, "text", sink); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.inner.outputPosition = math.MaxInt64 - 1
	called := false
	if err := s.Write(ctx, "zz", func(context.Context, MappedText) error { called = true; return nil }); !errors.Is(err, ErrStreamPositionOverflow) || called || s.inner.outputPosition != math.MaxInt64-1 {
		t.Fatal("mapped output overflow consumed", err, called)
	}
	s = NewMappedStream()
	s.inner.stages[0].resetAt(math.MaxInt64-1, 'x')
	if err := s.Write(ctx, "zz", sink); !errors.Is(err, ErrStreamPositionOverflow) {
		t.Fatal("mapped input overflow consumed", err)
	}
}

func TestMappedStreamDeadlineRollbackAndAcceptedCancellation(t *testing.T) {
	s := NewMappedStream()
	input := "password:\"private\" ordinary tail"
	if err := s.Write(context.Background(), "password:\"pri", func(context.Context, MappedText) error { t.Fatal("pending secret emitted output"); return nil }); err != nil {
		t.Fatal(err)
	}
	before := s.inner.stages[0].pos
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Write(cancelled, "ignored", func(context.Context, MappedText) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	deadline, cancelDeadline := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelDeadline()
	var rejected MappedText
	err := s.Write(deadline, "vate\" ordinary tail", func(ctx context.Context, b MappedText) error {
		rejected = MappedText{b.Text, slices.Clone(b.Spans)}
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) || s.inner.stages[0].pos != before || len(s.Counts()) != 0 || s.inner.outputPosition != 0 {
		t.Fatal("real deadline committed mapped transaction", err, s.Counts())
	}
	accepted, cancelAccepted := context.WithCancel(context.Background())
	offset := int64(0)
	err = s.Write(accepted, "vate\" ordinary tail", func(_ context.Context, b MappedText) error {
		if b.Text != rejected.Text || !slices.Equal(b.Spans, rejected.Spans) {
			t.Fatal("deadline retry changed origin")
		}
		checkMappedBatch(t, input, offset, b)
		offset += int64(len(b.Text))
		cancelAccepted()
		return nil
	})
	if err != nil || len(s.Counts()) != 1 || s.inner.outputPosition != offset {
		t.Fatal("successful sink cancellation did not commit", err)
	}
	if err := s.Finish(context.Background(), func(_ context.Context, b MappedText) error { checkMappedBatch(t, input, offset, b); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestMappedStreamFinishWithCommitRetainsEmptyEOFTransaction(t *testing.T) {
	s := NewMappedStream()
	calls := 0
	sink := func(_ context.Context, b MappedText) error {
		calls++
		if b.Text != "" || len(b.Spans) != 0 {
			t.Fatal("invented EOF text")
		}
		if calls == 1 {
			return context.DeadlineExceeded
		}
		return nil
	}
	if err := s.FinishWithCommit(context.Background(), sink); !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatal("empty EOF skipped commit transaction", err, calls)
	}
	if err := s.FinishWithCommit(context.Background(), sink); err != nil || calls != 2 {
		t.Fatal("empty EOF transaction was not retryable", err, calls)
	}
	if err := s.Write(context.Background(), "closed", func(context.Context, MappedText) error { return nil }); !errors.Is(err, ErrStreamClosed) {
		t.Fatal("successful EOF left stream open", err)
	}
}
