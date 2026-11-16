package semantic

import (
	"strings"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/capture"
)

// SplitText retains exact source-byte offsets. Overlap keeps nearby context
// across chunk boundaries; every byte remains represented, including Unicode.
func SplitText(text string) ([]Span, error) {
	if len(text) > capture.MaxObservationTextBytes || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return nil, ErrInvalid
	}
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	spans := make([]Span, 0, 1+len(text)/(ChunkBytes-ChunkOverlap))
	for start := 0; start < len(text); {
		end := min(start+ChunkBytes, len(text))
		for end < len(text) && !utf8.RuneStart(text[end]) {
			end--
		}
		if end < len(text) {
			floor := max(start+ChunkBytes/2, end-192)
			if boundary := strings.LastIndexAny(text[floor:end], "\n \t"); boundary >= 0 {
				end = floor + boundary + 1
			}
		}
		if end <= start || len(spans) >= MaxChunksPerSource {
			return nil, ErrInvalid
		}
		spans = append(spans, Span{Start: start, End: end})
		if end == len(text) {
			break
		}
		next := max(start+1, end-ChunkOverlap)
		for next < end && !utf8.RuneStart(text[next]) {
			next++
		}
		start = next
	}
	return spans, nil
}
