package privacy

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func streamResult(t *testing.T, parts []string) (string, []Count) {
	t.Helper()
	s := NewStream()
	defer s.Abort()
	var out strings.Builder
	emit := func(_ context.Context, text string) error {
		if !utf8.ValidString(text) {
			t.Fatal("invalid sanitized output")
		}
		out.WriteString(text)
		return nil
	}
	for _, part := range parts {
		if err := s.Write(context.Background(), part, emit); err != nil {
			t.Fatal(err)
		}
		for _, m := range s.stages {
			if len(m.pending) > streamPendingBytes || len(m.nfa.seeds) > len(m.pattern.program.Inst) {
				t.Fatal("unbounded retained stream state")
			}
		}
	}
	if err := s.Finish(context.Background(), emit); err != nil {
		t.Fatal(err)
	}
	return out.String(), s.Counts()
}
func streamPieces(input string, size int) []string {
	var parts []string
	for len(input) > 0 {
		n := min(size, len(input))
		if n < len(input) {
			for n > 0 && !utf8.RuneStart(input[n]) {
				n--
			}
		}
		if n == 0 {
			_, n = utf8.DecodeRuneInString(input)
		}
		parts = append(parts, input[:n])
		input = input[n:]
	}
	return parts
}

func TestStreamEveryRuleAcrossEveryValidSplit(t *testing.T) {
	inputs := []string{
		"ordinary note password: hunter2\nAuthorization: Bearer fixture\nhttps://alice:pw@example.test/p?token=abc&view=day\n",
		`{"password":"actual-secret","api_key":"value","token":"[REDACTED:SECRET]"} ordinary`,
		"password:\"two word secret\"; cvv: 123\nsecret=[REDACTED:SECRET]suffix\n",
		"sk-proj-123456789012345678901234567890 ghp_123456789012345678901234567890 AKIAIOSFODNN7EXAMPLE xoxb-123456789012345678901234---_tail",
		"SSN 078-05-1120 card 4111 1111 1111 1111 IBAN GB82 WEST 1234 5698 7654 32; invalid 4111 1111 1111 1112 and GB82 WEST 1234 5698 7654 33",
		"ordinary -----BEGIN RSA PRIVATE KEY-----\nfixture\n-----END PRIVATE KEY----- tail",
		"http://alice%ZZ%2540/path http:///p?%2574oken=actual-secret http:///path?token=[REDACTED:URL_QUERY]",
		"https://example.test/?client_secret=%5BREDACTED%3AURL_QUERY%5D&client_secret=actual-secret&view=day",
		"日本語 paſſword: π🙂, visible; xpassword: ordinary\n",
	}
	for _, in := range inputs {
		want, wantCounts := Redact(in)
		for cut := 0; cut <= len(in); cut++ {
			if !utf8.ValidString(in[:cut]) {
				continue
			}
			got, counts := streamResult(t, []string{in[:cut], in[cut:]})
			if got != want || !slices.Equal(counts, wantCounts) {
				t.Fatalf("split %d changed batch semantics: got %q %+v; want %q %+v", cut, got, counts, want, wantCounts)
			}
		}
	}
}
func TestStreamV2OwnsOutsideLabelURLsAndEscapedNewlines(t *testing.T) {
	cases := []struct{ in, want string }{
		{`password: "before https://example.test/path after"`, `password: "[REDACTED:SECRET]"`},
		{`password:https://example.test/path`, `password:[REDACTED:SECRET]`},
		{`https://example.test/password:ordinary?view=day`, `https://example.test/password:ordinary?view=day`},
		{"password: \"one\\\ntwo\" tail", "password: \"[REDACTED:SECRET]\" tail"},
		{"{\"password\":\"one\\\ntwo\"}", "{\"password\":\"[REDACTED:SECRET]\"}"},
		{"password: \"one\\\nunfinished", "[REDACTED:SECRET]"},
		{`password:"before https://example.test/path after-secret`, `[REDACTED:SECRET]`},
		{`{"password":"before https://example.test/path after-secret`, `{[REDACTED:SECRET]`},
		{"password:    \nvisible", "password:    \nvisible"},
	}
	for _, tc := range cases {
		for cut := 0; cut <= len(tc.in); cut++ {
			got, _ := streamResult(t, []string{tc.in[:cut], tc.in[cut:]})
			if got != tc.want {
				t.Fatal("v2 ownership failed", cut, got, tc.want)
			}
		}
	}
}
func TestStreamEarlierReplacementCreatesLaterSecretMatch(t *testing.T) {
	cases := []struct {
		in, want string
		counts   []Count
	}{
		{"password: -----BEGIN PRIVATE KEY-----\nmaterial\n-----END PRIVATE KEY-----hidden tail\nvisible", "password: [REDACTED:SECRET]\nvisible", []Count{{"PRIVATE_KEY", 1}, {"SECRET", 1}}},
		{"Authorization \n : -----BEGIN PRIVATE KEY-----\nmaterial\n-----END PRIVATE KEY-----hidden tail\nvisible", "[REDACTED:AUTHORIZATION]\nvisible", []Count{{"AUTHORIZATION", 1}, {"PRIVATE_KEY", 1}}},
	}
	for _, tc := range cases {
		got, counts := streamResult(t, streamPieces(tc.in, 1))
		if got != tc.want || !slices.Equal(counts, tc.counts) {
			t.Fatal("composition released a replacement-created secret", got, counts)
		}
	}
}
func TestStreamIncompleteSecretsAreMaskedOnlyOnFinishAndNeverAbort(t *testing.T) {
	for _, in := range []string{"before -----BEGIN PRIVATE KEY-----\nprivate-tail", "before password: \"private-tail", "before {\"token\":\"private-tail"} {
		s := NewStream()
		var out strings.Builder
		emit := func(_ context.Context, text string) error { out.WriteString(text); return nil }
		if err := s.Write(context.Background(), in, emit); err != nil {
			t.Fatal(err)
		}
		before := out.String()
		if strings.Contains(before, "private-tail") {
			t.Fatal("uncertain suffix escaped")
		}
		s.Abort()
		if out.String() != before {
			t.Fatal("Abort emitted pending text")
		}
		if err := s.Finish(context.Background(), emit); !errors.Is(err, ErrStreamClosed) {
			t.Fatal("Abort reopened stream", err)
		}
		got, counts := streamResult(t, []string{in})
		if strings.Contains(got, "private-tail") || len(counts) != 1 {
			t.Fatal("Finish released unfinished secret", got, counts)
		}
	}
}
func TestStreamOversizedFieldsRecoverAndKeepStateBounded(t *testing.T) {
	fill := strings.Repeat("a", 2*streamPendingBytes)
	cases := []struct{ in, want string }{
		{"password:\"" + fill + "\" ordinary tail", "[REDACTED:SECRET] ordinary tail"},
		{"-----BEGIN PRIVATE KEY-----" + fill + "-----END PRIVATE KEY----- ordinary tail", "[REDACTED:PRIVATE_KEY] ordinary tail"},
		{"https://example.test/" + fill + " ordinary tail", "[REDACTED:MALFORMED_URL] ordinary tail"},
		{"xoxb-aaaaaaaaaaaaaaaaaaaa" + strings.Repeat("-", 2*streamPendingBytes) + "ghp_bbbbbbbbbbbbbbbbbbbbbbbbb ordinary tail", "[REDACTED:TOKEN] ordinary tail"},
		{"-----BEGIN " + strings.Repeat("A", 2*streamPendingBytes) + " PASSWORD:\"hidden\ncontinued\"\nordinary tail", "[REDACTED:PRIVATE_KEY]\nordinary tail"},
	}
	for _, tc := range cases {
		got, _ := streamResult(t, streamPieces(tc.in, 4096))
		if got != tc.want {
			t.Fatal("oversized field did not recover ordinary tail", len(got), got[max(0, len(got)-100):])
		}
	}
}

func TestStreamOverflowRecoveryHonorsSensitiveQuoteGrammarAndEarlierReplacements(t *testing.T) {
	prefix := "-----BEGIN " + strings.Repeat("A", 128<<10)
	cases := []struct{ suffix, want string }{
		{" \"ordinary PASSWORD: \"hidden\ncontinued-secret-tail\nordinary tail", "[REDACTED:PRIVATE_KEY]"},
		{" PASSWORD: \"hidden Authorization: Bearer hidden\"\ncontinued-secret-tail\nordinary tail", "[REDACTED:PRIVATE_KEY]"},
		{" \"ordinary PASSWORD: \"hidden\ncontinued-secret-tail\"\nordinary tail", "[REDACTED:PRIVATE_KEY]\nordinary tail"},
		{" PASSWORD: \"hidden Authorization: Bearer hidden\"\ncontinued-secret-tail\"\nordinary tail", "[REDACTED:PRIVATE_KEY]\nordinary tail"},
		{" PASSWORD: \"hidden Authorization: Bearer -----BEGIN PRIVATE KEY-----\nkey \"ignored\n-----END PRIVATE KEY-----end auth\ncontinued-secret-tail\"\nordinary tail", "[REDACTED:PRIVATE_KEY]\nordinary tail"},
		{" https://example.test/password:\"ordinary\nvisible next record", "[REDACTED:PRIVATE_KEY]\nvisible next record"},
		{" https://p\"password\":\"hidden\ncontinued-secret-tail\nordinary tail", "[REDACTED:PRIVATE_KEY]"},
		{" HTTPS://p\"PASSWORD\":\"hidden\ncontinued-secret-tail\"\nordinary tail", "[REDACTED:PRIVATE_KEY]\nordinary tail"},
		{" https://example.test/password:\u00a0\"ordinary\nvisible next record", "[REDACTED:PRIVATE_KEY]\nvisible next record"},
		{" https://example.test/password:\f\"ordinary\nvisible next record", "[REDACTED:PRIVATE_KEY]\nvisible next record"},
		{" PASSWORD:\"before https://example.test/path after\ncontinued-secret-tail\nordinary tail", "[REDACTED:PRIVATE_KEY]"},
	}
	for _, tc := range cases {
		for _, size := range []int{97, 4096, MaxStreamInputBytes} {
			got, _ := streamResult(t, streamPieces(prefix+tc.suffix, size))
			if got != tc.want {
				t.Fatal("recovery exposed a hidden sensitive suffix or lost the genuine delimiter", size, got)
			}
		}
	}
}

func TestStreamV2PreservesOriginalUnicodeStartBoundaryWhenReplacing(t *testing.T) {
	for _, input := range []string{`xſecret:"private-value"`, `xſecret:private-value`} {
		for cut := 0; cut <= len(input); cut++ {
			if !utf8.ValidString(input[:cut]) {
				continue
			}
			got, counts := streamResult(t, []string{input[:cut], input[cut:]})
			if strings.Contains(got, "private-value") || !slices.Equal(counts, []Count{{"SECRET", 1}}) {
				t.Fatal("replacement lost original Unicode boundary", cut, got, counts)
			}
		}
	}
}
func TestStreamLargeDocumentKeepsCompleteOrdinaryTail(t *testing.T) {
	in := strings.Repeat("Build notes: compare the scheduler and cache. 日本語🙂\n", 40000) + "TAIL_FACT: rollback owner is Mira."
	if len(in) <= 1<<20 {
		t.Fatal("large fixture too small")
	}
	got, counts := streamResult(t, streamPieces(in, MaxStreamInputBytes))
	if got != in || len(counts) != 0 {
		t.Fatal("large document lost ordinary content or tail", len(got), len(in), counts)
	}
}
func TestStreamRejectedOutputRollsBackAndRetriesExactlyOnce(t *testing.T) {
	s := NewStream()
	defer s.Abort()
	var out strings.Builder
	emit := func(_ context.Context, text string) error { out.WriteString(text); return nil }
	if err := s.Write(context.Background(), "prefix password:", emit); err != nil {
		t.Fatal(err)
	}
	previous := s.Counts()
	rejected := 0
	err := s.Write(context.Background(), " private\nvisible\n", func(context.Context, string) error {
		rejected++
		return errors.New("private /Users/fixture underlying error")
	})
	if !errors.Is(err, ErrStreamOutput) || strings.Contains(err.Error(), "fixture") || rejected != 1 || !slices.Equal(s.Counts(), previous) {
		t.Fatal("failed input leaked error or committed state", err, rejected, s.Counts())
	}
	if err := s.Write(context.Background(), " private\nvisible\n", emit); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(context.Background(), emit); err != nil {
		t.Fatal(err)
	}
	want, counts := Redact("prefix password: private\nvisible\n")
	if out.String() != want || !slices.Equal(s.Counts(), counts) {
		t.Fatal("retry repeated or lost accepted content", out.String(), s.Counts())
	}
}
func TestStreamDeadlineFailureRetriesButSinkSuccessCommits(t *testing.T) {
	for _, accept := range []bool{false, true} {
		s := NewStream()
		var out strings.Builder
		ctx, cancel := context.WithCancel(context.Background())
		emit := func(_ context.Context, text string) error { out.WriteString(text); return nil }
		err := s.Write(ctx, "password: private\nvisible\n", func(_ context.Context, text string) error {
			cancel()
			if !accept {
				return ctx.Err()
			}
			return emit(ctx, text)
		})
		if accept {
			if err != nil || len(s.Counts()) != 1 {
				t.Fatal("accepted sink was rolled back", err, s.Counts())
			}
		} else {
			if !errors.Is(err, context.Canceled) || len(s.Counts()) != 0 {
				t.Fatal("rejected deadline committed", err, s.Counts())
			}
			if err := s.Write(context.Background(), "password: private\nvisible\n", emit); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Finish(context.Background(), emit); err != nil {
			t.Fatal(err)
		}
		want, counts := Redact("password: private\nvisible\n")
		if out.String() != want || !slices.Equal(s.Counts(), counts) {
			t.Fatal("context retry repeated or skipped content", out.String(), s.Counts())
		}
		s.Abort()
	}
}

func TestStreamRealSinkDeadlineIsRetryable(t *testing.T) {
	s := NewStream()
	defer s.Abort()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := s.Write(ctx, "password: private\nvisible\n", func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() })
	if !errors.Is(err, context.DeadlineExceeded) || len(s.Counts()) != 0 {
		t.Fatal("real round deadline lost cause or committed state", err, s.Counts())
	}
	var out strings.Builder
	emit := func(_ context.Context, text string) error { out.WriteString(text); return nil }
	if err := s.Write(context.Background(), "password: private\nvisible\n", emit); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(context.Background(), emit); err != nil {
		t.Fatal(err)
	}
	want, counts := Redact("password: private\nvisible\n")
	if out.String() != want || !slices.Equal(s.Counts(), counts) {
		t.Fatal("deadline retry lost or repeated accepted content", out.String(), s.Counts())
	}
}

func TestStreamCancelledNoOutputInputLeavesOriginalPending(t *testing.T) {
	s := NewStream()
	defer s.Abort()
	var out strings.Builder
	emit := func(_ context.Context, text string) error { out.WriteString(text); return nil }
	if err := s.Write(context.Background(), `password:"`, emit); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Write(ctx, "discarded", emit); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled input accepted", err)
	}
	if err := s.Write(context.Background(), `kept"`, emit); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(context.Background(), emit); err != nil {
		t.Fatal(err)
	}
	if out.String() != `password:"[REDACTED:SECRET]"` || !slices.Equal(s.Counts(), []Count{{"SECRET", 1}}) {
		t.Fatal("cancelled input changed original pending state", out.String(), s.Counts())
	}
}
func TestStreamAbortDuringSinkCannotResurrectPendingState(t *testing.T) {
	for _, accept := range []bool{false, true} {
		s := NewStream()
		err := s.Write(context.Background(), "password: private\nvisible\n", func(context.Context, string) error {
			s.Abort()
			if accept {
				return nil
			}
			return errors.New("rejected")
		})
		if accept {
			if err != nil || len(s.Counts()) != 1 {
				t.Fatal("accepted abort lost committed counts", err, s.Counts())
			}
		} else if !errors.Is(err, ErrStreamOutput) || len(s.Counts()) != 0 {
			t.Fatal("rejected abort committed counts", err, s.Counts())
		}
		if err := s.Write(context.Background(), "later", func(context.Context, string) error { return nil }); !errors.Is(err, ErrStreamClosed) {
			t.Fatal("outer write resurrected aborted stream", err)
		}
		for _, m := range s.stages {
			if len(m.pending) > 0 {
				t.Fatal("Abort retained raw pending text")
			}
		}
	}
}
func TestStreamRejectsReentrancyInvalidInputAndCountOverflow(t *testing.T) {
	s := NewStream()
	defer s.Abort()
	emit := func(context.Context, string) error { return nil }
	for _, input := range []string{string([]byte{0xff}), "nul\x00value", strings.Repeat("x", MaxStreamInputBytes+1)} {
		if err := s.Write(context.Background(), input, emit); !errors.Is(err, ErrStreamInput) {
			t.Fatal("invalid input accepted", err)
		}
	}
	if err := s.Write(nil, "a", emit); !errors.Is(err, ErrStreamInput) { //nolint:staticcheck // Verify that the API rejects a nil context.
		t.Fatal("nil context accepted", err)
	}
	if err := s.Write(context.Background(), "a", nil); !errors.Is(err, ErrStreamInput) {
		t.Fatal("nil sink accepted", err)
	}
	if err := s.Write(context.Background(), "visible\n", func(context.Context, string) error {
		if err := s.Write(context.Background(), "nested", emit); !errors.Is(err, ErrStreamBusy) {
			t.Fatal("reentrant write accepted", err)
		}
		if err := s.Finish(context.Background(), emit); !errors.Is(err, ErrStreamBusy) {
			t.Fatal("reentrant Finish accepted", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.counts["SECRET"] = math.MaxInt
	emitted := false
	if err := s.Write(context.Background(), "password: private\nvisible\n", func(context.Context, string) error { emitted = true; return nil }); !errors.Is(err, ErrStreamCountOverflow) || emitted || s.counts["SECRET"] != math.MaxInt {
		t.Fatal("count overflow committed", err, emitted, s.counts)
	}
}
func TestStreamRandomChunkBoundariesPreserveBatchParity(t *testing.T) {
	rng := rand.New(rand.NewSource(73))
	atoms := []string{"ordinary 日本語🙂\n", "password: private\n", `{"api_key":"private"}` + "\n", "https://example.test/p?token=private&view=day\n", "078-05-1120 4111 1111 1111 1111\n", "-----BEGIN PRIVATE KEY-----\nprivate\n-----END PRIVATE KEY-----\n"}
	for run := 0; run < 40; run++ {
		var in strings.Builder
		for i := 0; i < 12; i++ {
			in.WriteString(atoms[rng.Intn(len(atoms))])
		}
		input := in.String()
		var parts []string
		for len(input) > 0 {
			n := min(4+rng.Intn(31), len(input))
			if n < len(input) {
				for n > 0 && !utf8.RuneStart(input[n]) {
					n--
				}
			}
			parts = append(parts, input[:n])
			input = input[n:]
		}
		got, counts := streamResult(t, parts)
		want, wantCounts := Redact(in.String())
		if got != want || !slices.Equal(counts, wantCounts) {
			t.Fatal("chunk schedule changed redaction", run, got, counts, want, wantCounts)
		}
	}
}

func TestStreamExactPendingBoundDoesNotPrematurelyMaskURL(t *testing.T) {
	input := "https://example.test/" + strings.Repeat("x", streamPendingBytes-len("https://example.test/"))
	got, counts := streamResult(t, streamPieces(input+" tail", MaxStreamInputBytes))
	if got != input+" tail" || len(counts) != 0 {
		t.Fatal("an exactly bounded benign URL was treated as oversized", len(got), counts)
	}
}

func TestStreamFinishFailureRetainsPendingStateForRetry(t *testing.T) {
	s := NewStream()
	defer s.Abort()
	var out strings.Builder
	emit := func(_ context.Context, text string) error { out.WriteString(text); return nil }
	if err := s.Write(context.Background(), "ordinary password: \"unfinished", emit); err != nil {
		t.Fatal(err)
	}
	before := out.String()
	if err := s.Finish(context.Background(), func(context.Context, string) error { return errors.New("rejected") }); !errors.Is(err, ErrStreamOutput) {
		t.Fatal("rejected Finish did not retain state", err)
	}
	if out.String() != before || len(s.Counts()) != 0 {
		t.Fatal("failed Finish committed output or counts")
	}
	if err := s.Finish(context.Background(), emit); err != nil {
		t.Fatal(err)
	}
	if out.String() != "ordinary [REDACTED:SECRET]" || !slices.Equal(s.Counts(), []Count{{"SECRET", 1}}) {
		t.Fatal("Finish retry lost or repeated content", out.String(), s.Counts())
	}
	if err := s.Finish(context.Background(), emit); !errors.Is(err, ErrStreamClosed) {
		t.Fatal("finished stream reopened", err)
	}
	if err := s.Write(context.Background(), "later", emit); !errors.Is(err, ErrStreamClosed) {
		t.Fatal("finished stream accepted input", err)
	}
}

func TestStreamV2SanitizedOutputIsStableAndCountsAreIndependent(t *testing.T) {
	input := "password: \"https://example.test/p?token=private\"\ncvv=123\n-----BEGIN PRIVATE KEY-----\nprivate\n-----END PRIVATE KEY-----\n"
	first, counts := streamResult(t, streamPieces(input, 7))
	second, later := streamResult(t, streamPieces(first, 5))
	if first != second || len(later) != 0 || len(counts) < 3 {
		t.Fatal("sanitized output changed on reprocessing", first, second, counts, later)
	}
	s := NewStream()
	defer s.Abort()
	emit := func(context.Context, string) error { return nil }
	if err := s.Write(context.Background(), "cvv=123\npassword=private\n", emit); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(context.Background(), emit); err != nil {
		t.Fatal(err)
	}
	snapshot := s.Counts()
	snapshot[0].Count = 999
	if !slices.Equal(s.Counts(), []Count{{"SECRET", 1}, {"SECURITY_CODE", 1}}) {
		t.Fatal("counts were unsorted or shared with caller", s.Counts())
	}
}

func TestStreamMalformedURLReportsOnlyTheCommittedWholeURLMask(t *testing.T) {
	// The first parse accepts %25ZZ as a key; the required second decode
	// rejects its resulting %ZZ. Map iteration must not change the receipt.
	for attempt := 0; attempt < 128; attempt++ {
		got, counts := streamResult(t, []string{"https://alice:pw@example.test/?token=private&%25ZZ=x"})
		if got != "[REDACTED:MALFORMED_URL]" || !slices.Equal(counts, []Count{{"MALFORMED_URL", 1}}) {
			t.Fatal("receipt counted changes discarded by the whole URL mask", attempt, got, counts)
		}
	}
}
func BenchmarkStreamLargePlainDocument(b *testing.B) {
	input := strings.Repeat("Build notes and ordinary technical prose. 日本語🙂\n", 20000)
	parts := streamPieces(input, MaxStreamInputBytes)
	b.SetBytes(int64(len(input)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := NewStream()
		emit := func(context.Context, string) error { return nil }
		for _, p := range parts {
			if err := s.Write(context.Background(), p, emit); err != nil {
				b.Fatal(err)
			}
		}
		if err := s.Finish(context.Background(), emit); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRedactLargePlainDocument(b *testing.B) {
	input := strings.Repeat("Build notes and ordinary technical prose. 日本語🙂\n", 20000)
	b.SetBytes(int64(len(input)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Redact(input)
	}
}
