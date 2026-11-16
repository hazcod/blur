package assistant

import (
	"context"
	"strings"
	"unicode/utf8"
)

const (
	excerptHeadBytes  = 1200
	excerptTailBytes  = 400
	excerptSentenceBy = 200
)

// documentEnds keeps only what an extractive answer needs from a streamed
// document: its beginning and its end. Memory use is constant in document size.
type documentEnds struct {
	head, tail string
	total      int
}

func (e *documentEnds) add(text string) {
	e.total += len(text)
	if len(e.head) < excerptHeadBytes+excerptSentenceBy {
		room := excerptHeadBytes + excerptSentenceBy - len(e.head)
		if room > len(text) {
			room = len(text)
		}
		e.head += text[:room]
	}
	e.tail += text
	if keep := excerptTailBytes + excerptSentenceBy; len(e.tail) > keep {
		e.tail = e.tail[len(e.tail)-keep:]
	}
}

// excerpt returns the labelled-free extract: the first excerptHeadBytes and the
// last excerptTailBytes of the document, both cut on rune boundaries and, when
// a sentence end lies within excerptSentenceBy bytes of the cut, on that end.
func (e *documentEnds) excerpt() (head, tail string, whole bool) {
	if e.total <= excerptHeadBytes+excerptTailBytes {
		if e.total <= len(e.head) {
			return strings.TrimSpace(e.head[:e.total]), "", true
		}
		// The head buffer is shorter than the document only when it is larger
		// than the whole budget, so this cannot happen; stay safe regardless.
		return strings.TrimSpace(e.head), "", true
	}
	head = e.head[:excerptHeadBytes]
	cut := len(head)
	for cut > 0 && cut < len(e.head) && !utf8.RuneStart(e.head[cut]) {
		cut--
	}
	head = e.head[:cut]
	if end := lastSentenceEnd(head, excerptSentenceBy); end > 0 {
		head = head[:end]
	}
	tail = e.tail[len(e.tail)-excerptTailBytes:]
	skip := 0
	for skip < len(tail) && !utf8.RuneStart(tail[skip]) {
		skip++
	}
	tail = tail[skip:]
	if start := firstSentenceStart(tail, excerptSentenceBy); start > 0 {
		tail = tail[start:]
	}
	return strings.TrimSpace(head), strings.TrimSpace(tail), false
}

func isSentenceEnd(text string, i int) bool {
	switch text[i] {
	case '.', '!', '?':
		return i+1 == len(text) || text[i+1] == ' ' || text[i+1] == '\n' || text[i+1] == '\t'
	}
	return false
}

// lastSentenceEnd returns the offset just after the last sentence end within
// the final window bytes of text, or 0.
func lastSentenceEnd(text string, window int) int {
	for i := len(text) - 1; i >= 0 && i >= len(text)-window; i-- {
		if isSentenceEnd(text, i) {
			return i + 1
		}
	}
	return 0
}

// firstSentenceStart returns the offset of the first word after a sentence end
// found within the first window bytes of text, or 0.
func firstSentenceStart(text string, window int) int {
	for i := 0; i < len(text) && i < window; i++ {
		if isSentenceEnd(text, i) && i+1 < len(text) {
			j := i + 1
			for j < len(text) && (text[j] == ' ' || text[j] == '\n' || text[j] == '\t') {
				j++
			}
			return j
		}
	}
	return 0
}

// extractiveWholeDocument answers a whole-document request without any model.
// It reads the selected capture's authenticated parts through the normal
// reader, keeps only the beginning and end, and says plainly that this is an
// excerpt rather than a summary.
func (s *Service) extractiveWholeDocument(ctx context.Context, plan Plan, selected Evidence) (Result, error) {
	var ends documentEnds
	var partsRead, lastIndex int64 = 0, -1
	readOne := func(source Evidence) error {
		if source.Layer != LayerSource || !validSourceID(source.SourceID) || source.Text == "" || !utf8.ValidString(source.Text) || strings.ContainsRune(source.Text, 0) {
			return ErrContext
		}
		if selected.Document.PartCount > 0 && (source.Document.EditionID != selected.Document.EditionID || source.Document.PartIndex != lastIndex+1) {
			return ErrContext
		}
		lastIndex = source.Document.PartIndex
		partsRead++
		ends.add(source.Text)
		return ctx.Err()
	}
	if selected.Document.PartCount > 0 {
		reader, ok := s.retriever.(WholeDocumentPartReader)
		if !ok {
			return Result{}, ErrAskUnavailable
		}
		if err := reader.StreamWholeDocumentParts(ctx, selected, readOne); err != nil {
			return Result{}, err
		}
		if partsRead != selected.Document.PartCount || int64(ends.total) != selected.Document.TotalBytes {
			return Result{}, ErrContext
		}
	} else if err := readOne(selected); err != nil {
		return Result{}, err
	}
	head, tail, whole := ends.excerpt()
	var text strings.Builder
	text.WriteString("No AI model is configured, so I can’t summarize this page. ")
	if whole {
		text.WriteString("Here is its saved text:\n\n“" + head + "”")
	} else {
		text.WriteString("Here is an excerpt of its beginning and end:\n\n“" + head + " …”\n\n“… " + tail + "”")
	}
	if selected.Partial {
		text.WriteString("\n\nThis page was only partially captured.")
	}
	answer, err := ValidateAnswer(ModelAnswer{Text: text.String(), SourceIDs: []string{selected.SourceID}}, []string{selected.SourceID})
	if err != nil {
		return Result{}, err
	}
	return Result{Answer: answer, Sources: []Evidence{selected}, Direct: true, Plan: plan, CoverageLimited: true}, nil
}
