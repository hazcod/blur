package search

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// This differential check generates valid Unicode text, physical part layouts,
// work quanta and process checkpoints from each mutated input. NormalizeTokens
// is the query-side oracle; no expected index terms come from the stream.
func FuzzCaptureTokenStreamMatchesQueriesAfterCheckpoints(f *testing.F) {
	alphabet := []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 _-./'’\n\táéäåøßİΩЙかが日本語한각\u0301\u0308\u034f\u3099")
	encode := func(text string) []byte {
		out := make([]byte, 0, len(text))
		for _, r := range text {
			for i, candidate := range alphabet {
				if r == candidate {
					out = append(out, byte(i))
					break
				}
			}
		}
		return out
	}
	for _, seed := range []string{
		"", "agent_build_01 finished quartz calibration.",
		"Dima's café 日本語 résumé", "it'sa Dima’s2 Dima’s end",
		"각 한 か\u3099 A\u0301\u0308Z Й",
		"가 ", "か\u3099 ", "a\u0301 ",
		"W" + strings.Repeat("ᆨ\u0301", 20) + "ᆨ ",
		"a" + strings.Repeat("\u0301", 40) + "b",
	} {
		f.Add(encode(seed), []byte{0, 1, 3, 7, 29})
	}
	f.Fuzz(func(t *testing.T, raw, layout []byte) {
		// This bounds one fuzz experiment; storage permits arbitrarily many
		// such parts. The generator never rejects a byte for invalid Unicode.
		if len(raw) > 1024 || len(layout) > 256 {
			t.Skip()
		}
		var input strings.Builder
		for _, b := range raw {
			input.WriteRune(alphabet[int(b)%len(alphabet)])
		}
		text := input.String()
		want, err := NormalizeTokens(text, len(text)+1)
		if err != nil {
			t.Fatal(err)
		}
		expected := make(map[[32]byte]bool, len(want))
		for _, term := range want {
			expected[CaptureTermDigest(term)] = true
		}
		choice := func(i int) int {
			if len(layout) == 0 {
				return 0
			}
			return int(layout[i%len(layout)])
		}
		var stream CaptureTokenStream
		got := make(map[[32]byte]bool)
		var starts, ends []int
		start, part, quantum := 0, int64(0), 0
		for !stream.Complete {
			end := start
			for runes := 0; end < len(text) && runes < 1+choice(int(part))%31; runes++ {
				_, size := utf8.DecodeRuneInString(text[end:])
				end += size
			}
			starts, ends = append(starts, start), append(ends, end)
			offset := start
			for offset < end || end == len(text) && !stream.Complete {
				budget := 4 + choice(quantum)%13
				used, tokens, err := stream.Feed(text[offset:end], int64(offset), part, end == len(text), budget)
				if err != nil || used == 0 && len(tokens) == 0 && !stream.Complete {
					t.Fatalf("stream stopped at part=%d offset=%d: used=%d tokens=%d err=%v", part, offset, used, len(tokens), err)
				}
				if len(tokens) > budget {
					t.Fatalf("token quantum exceeded its work envelope: got=%d budget=%d", len(tokens), budget)
				}
				offset += used
				for _, token := range tokens {
					if token.Start < 0 || token.End <= token.Start || token.End > int64(len(text)) || !utf8.ValidString(text[token.Start:token.End]) || token.FirstPart < 0 || token.LastPart < token.FirstPart || token.LastPart > part {
						t.Fatalf("invalid original span: %+v", token)
					}
					if token.Start < int64(starts[token.FirstPart]) || token.Start >= int64(ends[token.FirstPart]) || token.End <= int64(starts[token.LastPart]) || token.End > int64(ends[token.LastPart]) {
						t.Fatalf("token span disagrees with original part membership: %+v starts=%v ends=%v", token, starts, ends)
					}
					originalTerms, err := NormalizeTokens(text[token.Start:token.End], len(text)+1)
					supported := false
					for _, term := range originalTerms {
						supported = supported || CaptureTermDigest(term) == token.Digest
					}
					if err != nil || !supported {
						t.Fatalf("token digest is unsupported by its original span: token=%+v original=%q terms=%q err=%v", token, text[token.Start:token.End], originalTerms, err)
					}
					got[token.Digest] = true
				}
				checkpoint, err := json.Marshal(stream)
				if err != nil {
					t.Fatal(err)
				}
				var reopened CaptureTokenStream
				if err := json.Unmarshal(checkpoint, &reopened); err != nil || !reopened.Valid() {
					t.Fatal("invalid persisted cursor", err)
				}
				stream = reopened
				quantum++
			}
			start, part = end, part+1
		}
		if len(got) != len(expected) {
			t.Fatalf("query/index term count differs: got=%d want=%d text=%q", len(got), len(expected), text)
		}
		for digest := range expected {
			if !got[digest] {
				t.Fatalf("query term missing after chunking/checkpoints: text=%q", text)
			}
		}
	})
}
