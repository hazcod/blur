package privacy

import (
	"context"
	"errors"
	"regexp"
	"regexp/syntax"
	"strings"
	"testing"
	"unicode/utf8"
)

func matcherResult(t *testing.T, p *streamPattern, pieces []string) (string, int) {
	t.Helper()
	m := newStreamMatcher(p)
	defer m.clear()
	var out strings.Builder
	forced := 0
	plain := func(s string) error {
		if !utf8.ValidString(s) || len(s) > streamForwardBytes {
			t.Fatal("unsafe or oversized plaintext forwarding")
		}
		out.WriteString(s)
		return nil
	}
	match := func(s string, mask bool) error {
		if len(s) > streamPendingBytes || !utf8.ValidString(s) {
			t.Fatal("unbounded or invalid match buffer")
		}
		if mask {
			forced++
		} else if p.re != token {
			// A token can end in '-' with an original following '_' providing
			// its word boundary; a standalone substring changes that context.
			if loc := p.re.FindStringIndex(s); loc == nil || loc[0] != 0 || loc[1] != len(s) {
				t.Fatalf("resolved text was not exactly one policy match: %q", s)
			}
		}
		out.WriteString("[MASK]")
		return nil
	}
	for _, s := range pieces {
		if err := m.feed(context.Background(), s, false, plain, match); err != nil {
			t.Fatal(err)
		}
		if len(m.pending) > streamPendingBytes {
			t.Fatal("candidate state exceeded its bound", len(m.pending))
		}
	}
	if err := m.feed(context.Background(), "", true, plain, match); err != nil {
		t.Fatal(err)
	}
	return out.String(), forced
}

func TestStreamMatcherSplitMatchesAndRealBoundaryContext(t *testing.T) {
	cases := []struct {
		name     string
		re       *regexp.Regexp
		in, want string
	}{
		{"label", label, "prefix password: one, suffix", "prefix [MASK], suffix"},
		{"quoted", quotedLabel, "prefix password: \"one\\\"two\" suffix", "prefix [MASK] suffix"},
		{"auth", auth, "Authorization \n : \n Bearer fixture\nordinary", "[MASK]\nordinary"},
		{"URL", webURL, "prefix https://example.test/?token=x tail", "prefix [MASK] tail"},
		{"token overhang", token, "sk-123456789012345678901234---/ghp_123456789012345678901234 tail", "[MASK]---/[MASK] tail"},
		{"token next-word assertion", token, "xoxb-aaaaaaaaaaaaaaaaaaaa---_tail", "[MASK]_tail"},
		{"fixed token extra word", token, "AKIAIOSFODNN7EXAMPLEX tail", "AKIAIOSFODNN7EXAMPLEX tail"},
		{"word boundary", label, "xpassword: ordinary, password: private", "xpassword: ordinary, [MASK]"},
		{"competing prefixes", auth, "Authorization  ordinary Authorization: private\nvisible", "Authorization  ordinary [MASK]\nvisible"},
		{"fold and UTF8", label, "日本語 paſſword: π🙂, visible", "日本語 [MASK], visible"},
		{"SSN", ssn, "x078-05-1120 078-05-1120!", "x078-05-1120 [MASK]!"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newStreamPattern(tc.re, false, nil)
			for cut := 0; cut <= len(tc.in); cut++ {
				if !utf8.ValidString(tc.in[:cut]) {
					continue
				}
				got, forced := matcherResult(t, p, []string{tc.in[:cut], tc.in[cut:]})
				if got != tc.want || forced != 0 {
					t.Fatalf("cut %d: got %q forced %d; want %q", cut, got, forced, tc.want)
				}
			}
		})
	}
}

func TestStreamMatcherLazyKeyResolvesAtFirstFooterAndKeepsTail(t *testing.T) {
	in := "before -----BEGIN PRIVATE KEY-----\nfirst\n-----END PRIVATE KEY----- ordinary -----BEGIN RSA PRIVATE KEY-----second-----END PRIVATE KEY----- tail"
	p := newStreamPattern(pem, true, regexp.MustCompile(`^-----BEGIN (?:[A-Z0-9 ]* )?PRIVATE KEY-----`))
	for cut := 0; cut <= len(in); cut++ {
		got, forced := matcherResult(t, p, []string{in[:cut], in[cut:]})
		if got != "before [MASK] ordinary [MASK] tail" || forced != 0 {
			t.Fatal("lazy key consumed later prose or retained a closed key", cut, got, forced)
		}
	}
}

func TestStreamMatcherFiniteGreedyReplayMatchesPolicy(t *testing.T) {
	in := strings.Repeat("1-", 65) + " GB82 WEST 1234 5698 7654 32 more 078-05-1120"
	for _, re := range []*regexp.Regexp{card, iban, ssn} {
		want := re.ReplaceAllString(in, "[MASK]")
		p := newStreamPattern(re, false, nil)
		var pieces []string
		for _, r := range in {
			pieces = append(pieces, string(r))
		}
		got, forced := matcherResult(t, p, pieces)
		if got != want || forced != 0 {
			t.Fatal("accepted-end replay changed finite policy", got, want, forced)
		}
	}
}

func TestStreamMatcherMasksOpenedSecretAtEOF(t *testing.T) {
	cases := []struct {
		re, opened *regexp.Regexp
		in         string
	}{
		{pem, regexp.MustCompile(`^-----BEGIN (?:[A-Z0-9 ]* )?PRIVATE KEY-----`), "before -----BEGIN PRIVATE KEY-----\nunfinished"},
		{quotedLabel, regexp.MustCompile(`(?i)^(?:password|secret|token)\b[ \t]*[:=][ \t]*"`), "before password: \"unfinished\\"},
	}
	for _, tc := range cases {
		got, forced := matcherResult(t, newStreamPattern(tc.re, tc.re == pem, tc.opened), []string{tc.in})
		if got != "before [MASK]" || forced != 1 {
			t.Fatal("unfinished known secret was emitted", got, forced)
		}
	}
}

func TestStreamMatcherOverflowMasksUnitAndResumesOrdinaryText(t *testing.T) {
	cases := []struct {
		name                    string
		re                      *regexp.Regexp
		lazy                    bool
		prefix, body, end, want string
	}{
		{"URL", webURL, false, "https://example.test/", "x", " ordinary tail", "[MASK] ordinary tail"},
		{"quoted", quotedLabel, false, "password:\"", "a", "\" ordinary tail", "[MASK] ordinary tail"},
		{"authorization header", auth, false, "Authorization", " ", "X masked record\nordinary tail", "[MASK]\nordinary tail"},
		{"key header", pem, true, "-----BEGIN ", "A", "? masked record\nordinary tail", "[MASK]\nordinary tail"},
		{"key body", pem, true, "-----BEGIN PRIVATE KEY-----", "a", "-----END PRIVATE KEY----- ordinary tail", "[MASK] ordinary tail"},
		{"token suffix", token, false, "sk-123456789012345678901234", "-", "/ ordinary tail", "[MASK]/ ordinary tail"},
		{"later credential in overhang", token, false, "xoxb-aaaaaaaaaaaaaaaaaaaa", "-", "ghp_bbbbbbbbbbbbbbbbbbbbbbbbb ordinary tail", "[MASK] ordinary tail"},
		{"hidden quoted label", pem, true, "-----BEGIN ", "A", " PASSWORD: \"hidden\ncontinued\"\nordinary tail", "[MASK]\nordinary tail"},
		{"hidden auth header", pem, true, "-----BEGIN ", "A", " Authorization \n : \n Bearer hidden\nordinary tail", "[MASK]\nordinary tail"},
		{"key replacement in hidden label", pem, true, "-----BEGIN ", "A", " PASSWORD: -----BEGIN PRIVATE KEY-----\nmaterial \"ignored\n-----END PRIVATE KEY-----hidden tail\nordinary tail", "[MASK]\nordinary tail"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pieces := []string{tc.prefix}
			for i := 0; i < 130; i++ {
				pieces = append(pieces, strings.Repeat(tc.body, 1024))
			}
			pieces = append(pieces, tc.end)
			got, forced := matcherResult(t, newStreamPattern(tc.re, tc.lazy, nil), pieces)
			if got != tc.want || forced != 1 {
				t.Fatal("oversized unit failed to recover", got, forced)
			}
		})
	}
}

func TestStreamMatcherKeepsOnlyProgramSizedViabilityState(t *testing.T) {
	m := newStreamMatcher(newStreamPattern(webURL, false, nil))
	defer m.clear()
	ignore := func(string) error { return nil }
	for i := 0; i < 1000; i++ {
		if err := m.feed(context.Background(), "https://", false, ignore, func(string, bool) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if len(m.nfa.seeds) > len(m.pattern.program.Inst) {
			t.Fatal("later starts accumulated unbounded viability state", i, len(m.nfa.seeds), len(m.pattern.program.Inst))
		}
	}
}

func TestStreamMatcherCoalescesViableStartsBeforeTheNextRune(t *testing.T) {
	p := newStreamPattern(webURL, false, nil)
	var body uint32
	for pc, inst := range p.program.Inst {
		if (inst.Op == syntax.InstRune || inst.Op == syntax.InstRuneAny) && inst.MatchRune('/') && inst.MatchRune('!') && inst.MatchRune('a') {
			body = uint32(pc)
			break
		}
	}
	if body == 0 {
		t.Fatal("URL body fixture unavailable")
	}
	// Two starts can reach the same consuming instruction. Their ordering
	// must not retain a second thread after both consume the next rune.
	n := streamNFA{seeds: []streamThread{{body, 0}, {body, 8}}}
	active, _ := n.advance(p.program, 'a', 'a', 32, false)
	if active != 0 || len(n.seeds) != 1 || n.seeds[0].start != 0 {
		t.Fatal("equivalent starts survived instead of coalescing", active, n.seeds)
	}
}

func TestStreamMatcherCloneOwnsPendingCandidate(t *testing.T) {
	m := newStreamMatcher(newStreamPattern(quotedLabel, false, nil))
	defer m.clear()
	ignore := func(string) error { return nil }
	var got []string
	matched := func(s string, forced bool) error {
		if forced {
			t.Fatal("unexpected forced match")
		}
		got = append(got, s)
		return nil
	}
	if err := m.feed(context.Background(), `password:"a`, false, ignore, matched); err != nil {
		t.Fatal(err)
	}
	c := m.clone()
	defer c.clear()
	if err := c.feed(context.Background(), `b"`, true, ignore, matched); err != nil {
		t.Fatal(err)
	}
	c.clear()
	if err := m.feed(context.Background(), `c"`, true, ignore, matched); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != `password:"ab"` || got[1] != `password:"ac"` {
		t.Fatal("clone shared or lost pending input", got)
	}
}

func TestStreamMatcherCancellationDoesNotEmit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := newStreamMatcher(newStreamPattern(label, false, nil))
	defer m.clear()
	emitted := false
	err := m.feed(ctx, "password: private", true, func(string) error { emitted = true; return nil }, func(string, bool) error { emitted = true; return nil })
	if !errors.Is(err, context.Canceled) || emitted {
		t.Fatal("cancelled matcher emitted or lost cause", err, emitted)
	}
}
