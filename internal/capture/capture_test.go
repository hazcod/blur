package capture

import (
	"context"
	"errors"
	"github.com/hazcod/blur/internal/privacy"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func capturePieces(text string, size int) []string {
	var out []string
	for len(text) > 0 {
		n := min(size, len(text))
		for n > 0 && n < len(text) && !utf8.RuneStart(text[n]) {
			n--
		}
		if n == 0 {
			_, n = utf8.DecodeRuneInString(text)
		}
		out = append(out, text[:n])
		text = text[n:]
	}
	return out
}
func newTestCaptureBuilder(t *testing.T) *CaptureBuilder {
	t.Helper()
	b, err := NewCaptureBuilder("com.example.editor", "example.test", strings.Repeat("a", 64), time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func buildCapture(t *testing.T, input string, size int) ([]CapturePart, CaptureSummary) {
	t.Helper()
	b := newTestCaptureBuilder(t)
	defer b.Abort()
	var parts []CapturePart
	sink := func(_ context.Context, batch []CapturePart) error {
		for _, p := range batch {
			if err := p.ValidateForAdmission(); err != nil {
				t.Fatal(err)
			}
			parts = append(parts, p)
		}
		return nil
	}
	for _, piece := range capturePieces(input, size) {
		if err := b.Write(context.Background(), piece, sink); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := b.Finish(context.Background(), CaptureComplete, sink)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ValidateForAdmission() != nil || summary.RawBytes() != int64(len(input)) || summary.PartCount() != int64(len(parts)) {
		t.Fatal("invalid summary", summary.PartCount(), len(parts))
	}
	return parts, summary
}
func joinedCapture(parts []CapturePart) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text())
	}
	return b.String()
}
func TestCaptureBuilderLargeUnicodeTailSplitSecretsAndCanonicalIdentity(t *testing.T) {
	input := strings.Repeat("Build notes 日本語🙂.\n", 50000) + "password:\"private-value\"\nTAIL_FACT: rollback owner is Mira."
	first, a := buildCapture(t, input, 4096)
	second, b := buildCapture(t, input, privacy.MaxStreamInputBytes)
	if !strings.HasSuffix(joinedCapture(first), "TAIL_FACT: rollback owner is Mira.") || strings.Contains(joinedCapture(first), "private-value") || a.ContentID() != b.ContentID() || a.Root() != b.Root() || a.Header().CaptureID() == b.Header().CaptureID() {
		t.Fatal("large capture lost tail, leaked secret, or changed canonical edition")
	}
	root := a.Header().InitialRoot()
	offset := int64(0)
	for i, p := range first {
		if p.Index() != int64(i) || p.Offset() != offset || len(p.Text()) > MaxCapturePartTextBytes || len(p.Origins()) > MaxCapturePartOrigins || !utf8.ValidString(p.Text()) {
			t.Fatal("invalid bounded part", i)
		}
		offset += int64(len(p.Text()))
		var err error
		root, err = AdvanceCaptureRoot(root, p)
		if err != nil {
			t.Fatal(err)
		}
		if p.Digest() != second[i].Digest() || !slices.Equal(p.Origins(), second[i].Origins()) {
			t.Fatal("part boundary depends on input call", i)
		}
	}
	if root != a.Root() || offset != a.Bytes() {
		t.Fatal("part membership chain does not bind complete capture")
	}
}
func TestCaptureBuilderEveryScalarBoundaryAndDenseOrigins(t *testing.T) {
	input := "日本語🙂 password:\"split secret\" ordinary tail"
	first, a := buildCapture(t, input, 1)
	for _, size := range []int{4, 7, 4096} {
		parts, s := buildCapture(t, input, size)
		if s.ContentID() != a.ContentID() || joinedCapture(parts) != joinedCapture(first) || !slices.Equal(parts[0].Origins(), first[0].Origins()) {
			t.Fatal("scalar pieces changed canonical output", size)
		}
	}
	dense := strings.Repeat("password:secret\n", 700)
	parts, s := buildCapture(t, dense, 7)
	if len(parts) < 2 || s.Bytes() >= MaxCapturePartTextBytes {
		t.Fatal("origin count did not produce independent bounded parts", len(parts), s.Bytes())
	}
	for _, p := range parts {
		if len(p.Origins()) > MaxCapturePartOrigins || p.Text() == "" {
			t.Fatal("dense origin overflow")
		}
	}
}
func TestCaptureBuilderChangedSecretLengthChangesOriginalLocations(t *testing.T) {
	a, sa := buildCapture(t, "password:\"a\" ordinary tail", 7)
	b, sb := buildCapture(t, "password:\"longer-secret\" ordinary tail", 7)
	if joinedCapture(a) != joinedCapture(b) || sa.ContentID() == sb.ContentID() {
		t.Fatal("identical visible text reused stale source locations")
	}
}
func TestCaptureAuthenticatedRestorationNeverGrantsWriteAdmission(t *testing.T) {
	parts, s := buildCapture(t, "password:\"private\" ordinary tail", 7)
	headerBytes, err := MarshalAuthenticatedCaptureHeader(s.Header())
	if err != nil {
		t.Fatal(err)
	}
	h, err := RestoreAuthenticatedCaptureHeader(headerBytes)
	if err != nil || h.Validate() != nil || h.ValidateForAdmission() == nil || h.FamilyID() != s.Header().FamilyID() {
		t.Fatal("header restoration admission", err)
	}
	partBytes, err := MarshalAuthenticatedCapturePart(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	p, err := RestoreAuthenticatedCapturePart(partBytes)
	if err != nil || p.Validate() != nil || p.ValidateForAdmission() == nil || p.Text() != parts[0].Text() {
		t.Fatal("part restoration admission", err)
	}
	copy := p.Origins()
	clear(copy)
	if p.Validate() != nil {
		t.Fatal("origin getter aliased restored part")
	}
	summaryBytes, err := MarshalAuthenticatedCaptureSummary(s)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreAuthenticatedCaptureSummary(summaryBytes)
	if err != nil || restored.Validate() != nil || restored.ValidateForAdmission() == nil || restored.ContentID() != s.ContentID() {
		t.Fatal("summary restoration admission", err)
	}
	counts := restored.Counts()
	clear(counts)
	if restored.Validate() != nil {
		t.Fatal("count getter aliased summary")
	}
	for _, bad := range [][]byte{nil, []byte(`{}`), append(slices.Clone(partBytes), byte(' ')), []byte(`{"version":1,"text":"raw"}`), bytesReplaceForTest(partBytes, "[REDACTED:SECRET]", "a-secret-value")} {
		if _, err := RestoreAuthenticatedCapturePart(bad); err == nil {
			t.Fatal("malformed part was restored")
		}
	}
	if _, err := RestoreAuthenticatedCapturePart(make([]byte, MaxAuthenticatedCapturePartBytes+1)); err == nil {
		t.Fatal("oversized record restored")
	}
}
func bytesReplaceForTest(b []byte, old, next string) []byte {
	return []byte(strings.Replace(string(b), old, next, 1))
}
func TestCaptureBuilderFinalPartialPartFailureIsRetryable(t *testing.T) {
	b := newTestCaptureBuilder(t)
	defer b.Abort()
	called := false
	if err := b.Write(context.Background(), "ordinary tail", func(context.Context, []CapturePart) error { called = true; return nil }); err != nil || called {
		t.Fatal("canonical partial part was emitted early", err)
	}
	var failed CapturePart
	if _, err := b.Finish(context.Background(), CaptureComplete, func(_ context.Context, p []CapturePart) error {
		if len(p) != 1 {
			t.Fatal("missing final partial")
		}
		failed = p[0]
		return context.DeadlineExceeded
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("final sink failure lost retry", err)
	}
	s, err := b.Finish(context.Background(), CaptureComplete, func(_ context.Context, p []CapturePart) error {
		if len(p) != 1 || p[0].Digest() != failed.Digest() || p[0].Text() != failed.Text() {
			t.Fatal("final retry changed part")
		}
		return nil
	})
	if err != nil || s.Bytes() != 13 || s.RawBytes() != 13 {
		t.Fatal("final retry lost accepted bytes", err, s.Bytes())
	}
	if err := b.Write(context.Background(), "late", func(context.Context, []CapturePart) error { return nil }); !errors.Is(err, ErrCaptureClosed) {
		t.Fatal("closed capture accepted write", err)
	}
}
func TestCaptureBuilderWriteRetryCallbackOwnershipAndAbort(t *testing.T) {
	input := strings.Repeat("ordinary text 日本語🙂.\n", 1800)
	b := newTestCaptureBuilder(t)
	var failed []CapturePart
	if err := b.Write(context.Background(), input, func(_ context.Context, p []CapturePart) error {
		failed = append([]CapturePart(nil), p...)
		return errors.New("private path /Users/alice/secret")
	}); !errors.Is(err, ErrCaptureOutput) {
		t.Fatal("unknown sink failure exposed", err)
	}
	if b.state.rawBytes != 0 {
		t.Fatal("rejected write advanced raw position")
	}
	var accepted []CapturePart
	err := b.Write(context.Background(), input, func(_ context.Context, p []CapturePart) error {
		if len(p) != len(failed) {
			t.Fatal("retry changed batch")
		}
		for i := range p {
			if p[i].Digest() != failed[i].Digest() {
				t.Fatal("retry changed digest")
			}
		}
		if err := b.Write(context.Background(), "reentry", nil); !errors.Is(err, ErrCaptureBusy) {
			t.Fatal("reentrant write accepted", err)
		}
		accepted = append(accepted, p...)
		clear(p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := b.Finish(context.Background(), CaptureComplete, func(_ context.Context, p []CapturePart) error { accepted = append(accepted, p...); return nil })
	if err != nil || joinedCapture(accepted) != input || s.Bytes() != int64(len(input)) {
		t.Fatal("mutable sink batch corrupted builder", err)
	}
	b = newTestCaptureBuilder(t)
	err = b.Write(context.Background(), input, func(context.Context, []CapturePart) error { b.Abort(); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Finish(context.Background(), CaptureComplete, func(context.Context, []CapturePart) error { return nil }); !errors.Is(err, ErrCaptureClosed) {
		t.Fatal("callback Abort resurrected capture", err)
	}
}
func TestCaptureBuilderValidationCancellationCoverageAndOverflow(t *testing.T) {
	ctx := context.Background()
	sink := func(context.Context, []CapturePart) error { return nil }
	if _, err := NewCaptureBuilder("bad app", "example.test", strings.Repeat("a", 64), time.Now()); err == nil {
		t.Fatal("invalid metadata admitted")
	}
	if _, err := NewCaptureBuilder("com.example.editor", "example.test", "raw source locator", time.Now()); err == nil {
		t.Fatal("raw locator admitted")
	}
	b := newTestCaptureBuilder(t)
	for _, raw := range []string{"\x00", "\xff", strings.Repeat("x", privacy.MaxStreamInputBytes+1)} {
		if err := b.Write(ctx, raw, sink); !errors.Is(err, ErrCaptureInvalid) {
			t.Fatal("invalid raw piece admitted", err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := b.Write(cancelled, "unused", sink); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was consumed", err)
	}
	if _, err := b.Finish(ctx, CaptureComplete, sink); err == nil {
		t.Fatal("empty capture finished")
	}
	if err := b.Write(ctx, "structure tail", sink); err != nil {
		t.Fatal(err)
	}
	s, err := b.Finish(ctx, CaptureStructureLimited, sink)
	if err != nil || s.Coverage() != CaptureStructureLimited {
		t.Fatal("coverage fabricated completeness", err)
	}
	b = newTestCaptureBuilder(t)
	b.state.rawBytes = math.MaxInt64 - 1
	if err := b.Write(ctx, "xx", sink); !errors.Is(err, ErrCaptureInvalid) || b.state.rawBytes != math.MaxInt64-1 {
		t.Fatal("raw byte counter wrapped", err)
	}
}

func TestCaptureFormatBindsRedactionPolicyAndRejectsOversizedRawMetadata(t *testing.T) {
	b := newTestCaptureBuilder(t)
	encoded, err := MarshalAuthenticatedCaptureHeader(b.Header())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"redaction_version":2`) {
		t.Fatal("capture header did not bind its redaction policy")
	}
	if _, err := NewCaptureBuilder("com.example.editor", strings.Repeat(" ", 300)+"example.test", strings.Repeat("a", 64), time.Now()); err == nil {
		t.Fatal("raw metadata bypassed bounds through normalization")
	}
}

func TestCaptureBuilderRealDeadlineRetryAndAcceptedCancellation(t *testing.T) {
	b := newTestCaptureBuilder(t)
	if err := b.Write(context.Background(), "ordinary tail", func(context.Context, []CapturePart) error { return nil }); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var failed CapturePart
	if _, err := b.Finish(deadline, CaptureComplete, func(ctx context.Context, parts []CapturePart) error {
		failed = parts[0]
		<-ctx.Done()
		return ctx.Err()
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("real deadline finalized capture", err)
	}
	accepted, cancelAccepted := context.WithCancel(context.Background())
	s, err := b.Finish(accepted, CaptureComplete, func(_ context.Context, parts []CapturePart) error {
		if len(parts) != 1 || parts[0].Digest() != failed.Digest() {
			t.Fatal("deadline retry changed pending part")
		}
		cancelAccepted()
		return nil
	})
	if err != nil || s.ValidateForAdmission() != nil {
		t.Fatal("successful sink cancellation lost capture commit", err)
	}
	b = newTestCaptureBuilder(t)
	if err := b.Write(context.Background(), "ordinary tail", func(context.Context, []CapturePart) error { return nil }); err != nil {
		t.Fatal(err)
	}
	s, err = b.Finish(context.Background(), CaptureComplete, func(context.Context, []CapturePart) error { b.Abort(); return nil })
	if !errors.Is(err, ErrCaptureClosed) || s.ValidateForAdmission() == nil {
		t.Fatal("Abort in final sink allowed publication", err)
	}
}
