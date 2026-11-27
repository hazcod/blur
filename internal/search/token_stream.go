package search

import (
	"crypto/sha256"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// CaptureTermDigest is a streaming digest of an already canonical term. Each
// step finalizes its hash, so a saved cursor contains no plaintext word tail.
// The durable capture index applies its secret lookup MAC to this digest.
func CaptureTermDigest(term string) [32]byte {
	h := captureTermSeed
	for _, r := range term {
		h = captureTermStep(h, r)
	}
	return h
}

var captureTermSeed = sha256.Sum256([]byte("blur.capture.token.v1\x00"))

func captureTermStep(previous [32]byte, r rune) [32]byte {
	var input [36]byte
	copy(input[:32], previous[:])
	n := utf8.EncodeRune(input[32:], r)
	return sha256.Sum256(input[:32+n])
}

type CaptureToken struct {
	Digest                          [32]byte
	Start, End, FirstPart, LastPart int64
}

// CaptureTokenStream is bounded resumable tokenizer state. Buffer is only the
// final Unicode normalization segment (at most norm.MaxSegmentSize bytes),
// never the unfinished word. Its text comes from already sanitized capture.
type CaptureTokenStream struct {
	Next                            int64
	Buffer                          string `json:",omitempty"`
	BufferStart, BufferPart         int64
	BufferParts                     []CaptureTokenPart `json:",omitempty"`
	FoldBase                        rune               `json:",omitempty"`
	PrefixCGJ                       bool               `json:",omitempty"`
	Hash                            [32]byte
	Start, End, FirstPart, LastPart int64
	Word                            bool `json:",omitempty"`
	Apostrophe, PossessiveS         bool `json:",omitempty"`
	SStart, SEnd, SPart             int64
	Complete                        bool `json:",omitempty"`
}

type CaptureTokenPart struct{ Start, Part int64 }

func (s CaptureTokenStream) partAt(offset int64) int64 {
	for i := len(s.BufferParts) - 1; i >= 0; i-- {
		if offset >= s.BufferParts[i].Start {
			return s.BufferParts[i].Part
		}
	}
	return s.BufferPart
}
func (s CaptureTokenStream) Valid() bool {
	if len(s.BufferParts) > 4*norm.MaxSegmentSize {
		return false
	}
	for i, p := range s.BufferParts {
		if p.Start < s.BufferStart || p.Start > s.Next || p.Part < 0 || i > 0 && (p.Start <= s.BufferParts[i-1].Start || p.Part < s.BufferParts[i-1].Part) {
			return false
		}
	}
	return s.Next >= 0 && s.BufferStart >= 0 && s.BufferStart <= s.Next && len(s.Buffer) <= 4*norm.MaxSegmentSize && utf8.ValidString(s.Buffer) && s.BufferPart >= 0 && s.Start >= 0 && s.End >= s.Start && s.End <= s.Next && s.FirstPart >= 0 && s.LastPart >= s.FirstPart && (!s.Apostrophe && !s.PossessiveS || s.Word) && !(s.Apostrophe && s.PossessiveS) && (!s.Complete || s.Buffer == "" && !s.Word)
}

// Feed consumes a bounded prefix and emits at most maxTokens complete tokens.
// Offsets always refer to the original text, including composed Unicode.
func (s *CaptureTokenStream) Feed(text string, offset, part int64, final bool, maxTokens int) (int, []CaptureToken, error) {
	if s == nil || !s.Valid() || s.Complete || offset != s.Next || part < 0 || maxTokens < 4 || !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 {
		return 0, nil, ErrInvalidRequest
	}
	var out []CaptureToken
	used := 0
	for used < len(text) && len(out) < maxTokens-2 {
		r, n := utf8.DecodeRuneInString(text[used:])
		if s.Buffer == "" {
			s.BufferStart = s.Next
			s.BufferPart = part
			s.BufferParts = nil
		}
		if len(s.BufferParts) == 0 || s.BufferParts[len(s.BufferParts)-1].Part != part {
			s.BufferParts = append(s.BufferParts, CaptureTokenPart{s.Next, part})
		}
		s.Buffer += string(r)
		s.Next += int64(n)
		used += n
		s.normalize(false, part, offset, &out)
	}
	if final && used == len(text) && len(out) < maxTokens-2 {
		s.normalize(true, part, offset, &out)
		s.flush(&out)
		s.Complete = true
	}
	if !s.Valid() {
		return 0, nil, ErrInvalidRequest
	}
	return used, out, nil
}

func (s *CaptureTokenStream) normalize(final bool, part, partOffset int64, out *[]CaptureToken) {
	input := s.Buffer
	prefix := 0
	if s.PrefixCGJ {
		input = "\u034f" + input
		prefix = len("\u034f")
	}
	type segment struct {
		start, end int
		value      string
	}
	var segments []segment
	var it norm.Iter
	it.InitString(norm.NFD, input)
	for !it.Done() {
		start := it.Pos()
		value := string(it.Next())
		segments = append(segments, segment{start, it.Pos(), value})
	}
	count := len(segments)
	if !final && count > 0 {
		last, _ := utf8.DecodeLastRuneInString(input)
		if !norm.NFD.PropertiesString(string(last)).BoundaryAfter() {
			count--
		}
	}
	consumed := 0
	for _, seg := range segments[:count] {
		start := s.BufferStart + int64(max(0, seg.start-prefix))
		end := s.BufferStart + int64(max(0, seg.end-prefix))
		first, last := s.partAt(start), s.partAt(max(start, end-1))
		var folded strings.Builder
		for _, r := range norm.NFD.String(strings.ToLower(seg.value)) {
			if IsCombiningAccent(r) && (unicode.Is(unicode.Latin, s.FoldBase) || unicode.Is(unicode.Greek, s.FoldBase)) {
				continue
			}
			s.FoldBase = r
			folded.WriteRune(r)
		}
		value := norm.NFC.String(folded.String())
		if value == "" && s.Word {
			s.End = end
			s.LastPart = last
		}
		for _, r := range value {
			// Same plain spelling as FoldLower; an expanded letter ("ß" to
			// "ss") keeps the span of the one source rune.
			if plain, ok := undecomposedLetter(r); ok {
				for _, p := range plain {
					s.rune(p, start, end, first, last, out)
				}
				continue
			}
			s.rune(r, start, end, first, last, out)
		}
		consumed = max(0, seg.end-prefix)
	}
	if consumed > 0 {
		owner := s.partAt(s.BufferStart + int64(consumed))
		s.Buffer = s.Buffer[consumed:]
		s.BufferStart += int64(consumed)
		s.BufferPart = owner
		parts := []CaptureTokenPart{{s.BufferStart, owner}}
		for _, p := range s.BufferParts {
			if p.Start > s.BufferStart {
				parts = append(parts, p)
			}
		}
		s.BufferParts = parts
	}
	s.PrefixCGJ = false
	if count < len(segments) {
		seg := segments[count]
		// Iter inserts a stream-safe CGJ before a 31st nonstarter. Retain
		// that normalization state without adding it to original spans.
		s.PrefixCGJ = strings.HasPrefix(seg.value, "\u034f") && !strings.HasPrefix(s.Buffer, "\u034f")
	}
	if s.Buffer == "" {
		s.BufferStart = s.Next
		s.BufferPart = part
		s.BufferParts = nil
		s.PrefixCGJ = false
	}
}
func (s *CaptureTokenStream) flush(out *[]CaptureToken) {
	if s.Word {
		*out = append(*out, CaptureToken{s.Hash, s.Start, s.End, s.FirstPart, s.LastPart})
	}
	s.Word = false
	s.Hash = [32]byte{}
	s.Apostrophe = false
	s.PossessiveS = false
}
func (s *CaptureTokenStream) add(r rune, start, end, first, last int64) {
	if !s.Word {
		s.Word = true
		s.Hash = captureTermSeed
		s.Start = start
		s.FirstPart = first
	}
	s.Hash = captureTermStep(s.Hash, r)
	s.End = end
	s.LastPart = last
}
func (s *CaptureTokenStream) rune(r rune, start, end, first, last int64, out *[]CaptureToken) {
	word := isTokenRune(r)
	if s.PossessiveS {
		s.PossessiveS = false
		if word {
			a, b, p := s.SStart, s.SEnd, s.SPart
			s.flush(out)
			s.add('s', a, b, p, p)
		} else {
			s.flush(out)
		}
	}
	if s.Apostrophe {
		s.Apostrophe = false
		if r == 's' {
			s.PossessiveS = true
			s.SStart = start
			s.SEnd = end
			s.SPart = first
			return
		}
		s.flush(out)
	}
	if word {
		s.add(r, start, end, first, last)
		return
	}
	if (r == '\'' || r == '’') && s.Word {
		s.Apostrophe = true
		return
	}
	s.flush(out)
}
