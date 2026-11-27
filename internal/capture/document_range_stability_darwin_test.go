//go:build darwin

package capture

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// Keep the structure stable while the real native CF/range conversion reads a
// mutable provider. No user windows or Accessibility permission are involved.
type nativeRangeCursorFixture struct{ *nativeAXNode }

func (n *nativeRangeCursorFixture) childrenPage(context.Context, int, int, int) ([]axTreeNode, int, error) {
	return nil, 0, nil
}
func (n *nativeRangeCursorFixture) childMatches(context.Context, int, int, uintptr) (bool, error) {
	return false, nil
}

func TestNativeRangeSameLengthEditCannotCompleteAMixedDocument(t *testing.T) {
	original := strings.Repeat("Visible owner Mira; release approved. ", 64)
	modified := strings.ReplaceAll(original, "Mira", "Noah")
	f := &alternateTextFixture{text: original, mode: "range", count: int64(len(utf16.Encode([]rune(original))))}
	n, _ := alternateNativeText(t, f)
	cursor, err := newAXDocumentCursor(&nativeRangeCursorFixture{n}, 4, func(context.Context) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cursor.close()
	limits := Limits{MaxBytes: 128, MaxNodes: 8, MaxDepth: 4, MaxDuration: time.Second}
	var got strings.Builder
	for round := 0; round < 1000; round++ {
		progress, err := cursor.read(context.Background(), limits, func(_ context.Context, text string, _ bool) error {
			got.WriteString(text)
			return nil
		})
		if err != nil {
			if !errors.Is(err, errBrowserPageChanged) {
				t.Fatalf("unexpected native range failure: %v", err)
			}
			return // changed content must not receive a complete document receipt
		}
		if progress.Done {
			if got.String() != original && got.String() != modified {
				t.Fatalf("same-length edit received a complete mixed document: bytes=%d original=%t modified=%t", got.Len(), got.String() == original, got.String() == modified)
			}
			return
		}
		if got.Len() > 0 {
			f.text = modified // same character count, same window/path/roles
		}
	}
	t.Fatal("native range did not terminate")
}

func nativeRangeDocumentJob(t *testing.T, raw string, guard func(context.Context) error) (*axCaptureDocumentJob, *alternateTextFixture, *readOnlyAXFixture) {
	t.Helper()
	f := &alternateTextFixture{text: raw, mode: "range", count: int64(len(utf16.Encode([]rune(raw))))}
	n, meta := alternateNativeText(t, f)
	cursor, err := newAXDocumentCursor(&nativeRangeCursorFixture{n}, 4, guard, nil)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewCaptureBuilder("org.example.range-editor", "", strings.Repeat("a", 64), time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC))
	if err != nil {
		cursor.close()
		t.Fatal(err)
	}
	job := &axCaptureDocumentJob{cursor: cursor, builder: builder, limits: Limits{MaxBytes: 128, MaxNodes: 8, MaxDepth: 4, MaxDuration: time.Second}, guard: guard}
	t.Cleanup(job.Close)
	return job, f, meta
}

// Removing verification progress accounting would time out a healthy job after
// three rounds with no emitted bytes. Charging those reads incorrectly would
// either double the summary size or perform unbounded work in a single round.
func TestNativeRangeVerificationRoundsPreserveWholeUnicodeDocument(t *testing.T) {
	raw := strings.Repeat("Owner Mira 日本語 🙂 e\u0301. ", 100) + "TAIL_EVIDENCE"
	job, f, _ := nativeRangeDocumentJob(t, raw, func(context.Context) error { return nil })
	var saved strings.Builder
	var reader *nativeAXDocumentRangeText
	units := utf16.Encode([]rune(raw))
	verificationRounds, totalBytes := 0, 0
	for round := 0; round < 1000; round++ {
		beforeReads := f.parameterReads
		var verifiedBefore int64
		if reader != nil {
			verifiedBefore = reader.verifyOffset
		}
		p, err := job.Read(context.Background(), func(_ context.Context, parts []CapturePart) error {
			for _, part := range parts {
				saved.WriteString(part.Text())
			}
			return nil
		})
		if err != nil {
			t.Fatal("stable document failed during bounded verification", err)
		}
		if reader == nil && len(job.cursor.stack) > 0 {
			reader, _ = job.cursor.stack[0].text.(*nativeAXDocumentRangeText)
		}
		verificationBytes := 0
		if reader != nil {
			verificationBytes = len(string(utf16.Decode(units[verifiedBefore:reader.verifyOffset])))
		}
		if p.Bytes+verificationBytes > 128 || f.parameterReads-beforeReads > 34 {
			t.Fatal("one round escaped native/byte work limits", p, f.parameterReads-beforeReads)
		}
		totalBytes += p.Bytes
		if p.Bytes == 0 && !p.Done && f.parameterReads > beforeReads {
			verificationRounds++
		}
		if p.Done {
			if p.Summary.ValidateForAdmission() != nil || p.Summary.Coverage() != CaptureComplete || p.Summary.RawBytes() != int64(len(raw)) || saved.String() != raw || totalBytes != len(raw) || verificationRounds < 4 {
				t.Fatalf("verification lost, repeated or prematurely published content: raw=%d emitted=%d stored=%d verificationRounds=%d", len(raw), totalBytes, saved.Len(), verificationRounds)
			}
			return
		}
		if p.Summary.Validate() == nil {
			t.Fatal("unverified document received an admissible receipt")
		}
	}
	t.Fatal("stable native document did not complete")
}

func TestNativeRangeVerificationFailureNeverAdmitsStagedDocument(t *testing.T) {
	for _, change := range []string{"same_length", "unavailable", "secure", "length"} {
		t.Run(change, func(t *testing.T) {
			raw := strings.Repeat("Owner Mira. ", 64) // six exact 128-byte data rounds
			job, f, meta := nativeRangeDocumentJob(t, raw, func(context.Context) error { return nil })
			emitted := 0
			for round := 0; round < 20; round++ {
				p, err := job.Read(context.Background(), func(context.Context, []CapturePart) error { return nil })
				if p.Done || p.Summary.Validate() == nil {
					t.Fatal("changed native document received a complete receipt")
				}
				if err != nil {
					if emitted != len(raw) {
						t.Fatal("fixture failed before its verification transition", err)
					}
					return
				}
				emitted += p.Bytes
				if emitted == len(raw) {
					switch change {
					case "same_length":
						f.text = strings.ReplaceAll(raw, "Mira", "Noah")
					case "unavailable":
						job.cursor.stack[0].node.(*nativeRangeCursorFixture).api.parameterizedValue = func(uintptr, uintptr, uintptr, *uintptr) int32 { return axAttributeUnsupported }
					case "secure":
						meta.subrole = "AXSecureTextArea"
					case "length":
						f.count++
					}
				}
			}
			t.Fatal("invalid verification neither failed nor completed")
		})
	}
}

func TestNativeRangeVerificationDeadlineResumesAndOwnerCancellationTerminates(t *testing.T) {
	for _, cancelOwner := range []bool{false, true} {
		t.Run(map[bool]string{false: "round_deadline", true: "owner_cancel"}[cancelOwner], func(t *testing.T) {
			owner, cancel := context.WithCancel(context.Background())
			defer cancel()
			var roundCtx context.Context
			job, f, _ := nativeRangeDocumentJob(t, strings.Repeat("Owner Mira. ", 64), func(ctx context.Context) error { roundCtx = ctx; return nil })
			job.limits.MaxDuration = 20 * time.Millisecond
			f.onParameter = func() {
				if f.parameterReads == 7 { // first verification read
					if cancelOwner {
						cancel()
					} else {
						<-roundCtx.Done()
					}
				}
			}
			var saved strings.Builder
			for round := 0; round < 30; round++ {
				p, err := job.Read(owner, func(_ context.Context, parts []CapturePart) error {
					for _, part := range parts {
						saved.WriteString(part.Text())
					}
					return nil
				})
				if err != nil {
					if !cancelOwner || !errors.Is(err, context.Canceled) || p.Done || p.Summary.Validate() == nil || f.parameterReads != 7 {
						t.Fatal("verification cancellation lost authority", p, err)
					}
					return
				}
				if p.Done {
					if cancelOwner || p.Summary.ValidateForAdmission() != nil || saved.String() != strings.Repeat("Owner Mira. ", 64) {
						t.Fatal("verification deadline lost source or canceled owner completed")
					}
					return
				}
			}
			t.Fatal("verification did not terminate")
		})
	}
}

func TestNativeRangeUnknownExtentKeepsVisibleSlicePartialAfterVerification(t *testing.T) {
	raw := "offscreen prefix. Visible 日本語 🙂. offscreen tail."
	visible := "Visible 日本語 🙂."
	provider := &alternateTextFixture{text: raw, mode: "range", visible: &nativeTextRange{Location: 18, Length: int64(len(utf16.Encode([]rune(visible))))}}
	node, _ := alternateNativeText(t, provider)
	copyValue := node.api.copyValue
	node.api.copyValue = func(element, attribute uintptr, out *uintptr) int32 {
		name, _, err := copyCFString(node.api, attribute, 64)
		if err != nil {
			t.Fatal(err)
		}
		if name == "AXNumberOfCharacters" {
			return axAttributeUnsupported
		}
		return copyValue(element, attribute, out)
	}
	cursor, err := newAXDocumentCursor(&nativeRangeCursorFixture{node}, 4, func(context.Context) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cursor.close()
	var got strings.Builder
	for round := 0; round < 100; round++ {
		p, err := cursor.read(context.Background(), Limits{MaxBytes: 8, MaxNodes: 8, MaxDepth: 4, MaxDuration: time.Second}, func(_ context.Context, text string, _ bool) error { got.WriteString(text); return nil })
		if err != nil {
			t.Fatal(err)
		}
		if p.Done {
			if !p.StructureLimited || got.String() != visible || provider.parameterReads < 2 {
				t.Fatal("verified visible-only slice expanded its coverage", p, got.String())
			}
			return
		}
	}
	t.Fatal("visible-only range did not terminate")
}
