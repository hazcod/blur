package privacy

import (
	"context"
	"errors"
	"math"
	"regexp"
	"regexp/syntax"
	"unicode/utf8"
)

const streamPendingBytes = 64 << 10
const streamForwardBytes = 16 << 10

var errStreamMatch = errors.New("privacy: stream matcher unavailable")

// ErrStreamPositionOverflow rejects input before its byte coordinates wrap.
var ErrStreamPositionOverflow = errors.New("privacy: stream position overflow")

var streamKeyOpened = regexp.MustCompile(`^-----BEGIN (?:[A-Z0-9 ]* )?PRIVATE KEY-----`)
var streamQuotedOpened = regexp.MustCompile(`(?i)^(password|secret|token|api[_ -]?key|cvv|cvc|security[_ -]?code)\b[ \t]*[:=][ \t]*"`)
var streamJSONOpened = regexp.MustCompile(`(?i)^"(password|secret|token|api[_ -]?key|cvv|cvc|security[_ -]?code)"[ \t]*:[ \t]*"`)

type streamPattern struct {
	re, opened       *regexp.Regexp
	program          *syntax.Prog
	first            []uint32
	ascii            [128]bool
	lazy, credential bool
}

func streamProgram(expression string) *syntax.Prog {
	parsed, err := syntax.Parse(expression, syntax.Perl)
	if err != nil {
		panic("privacy: invalid static stream pattern")
	}
	program, err := syntax.Compile(parsed.Simplify())
	if err != nil {
		panic("privacy: invalid static stream program")
	}
	return program
}

func newStreamPattern(re *regexp.Regexp, lazy bool, opened *regexp.Regexp) *streamPattern {
	if re == nil || re.MatchString("") {
		panic("privacy: empty static stream pattern")
	}
	if opened == nil {
		switch re {
		case pem:
			opened = streamKeyOpened
		case quotedLabel:
			opened = streamQuotedOpened
		case jsonLabel:
			opened = streamJSONOpened
		}
	}
	p := &streamPattern{re: re, opened: opened, program: streamProgram(re.String()), lazy: lazy, credential: re == token}
	seen := make([]bool, len(p.program.Inst))
	stack := []uint32{uint32(p.program.Start)}
	for len(stack) > 0 {
		pc := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if pc == 0 || seen[pc] {
			continue
		}
		seen[pc] = true
		i := &p.program.Inst[pc]
		switch i.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			stack = append(stack, i.Out, i.Arg)
		case syntax.InstCapture, syntax.InstEmptyWidth, syntax.InstNop:
			stack = append(stack, i.Out)
		case syntax.InstRune, syntax.InstRune1, syntax.InstRuneAny, syntax.InstRuneAnyNotNL:
			p.first = append(p.first, pc)
		}
	}
	for r := rune(0); r < 128; r++ {
		p.ascii[r] = p.starts(r)
	}
	return p
}
func streamRune(i *syntax.Inst, r rune) bool {
	if r < 0 {
		return false
	}
	switch i.Op {
	case syntax.InstRuneAny:
		return true
	case syntax.InstRuneAnyNotNL:
		return r != '\n'
	default:
		return i.MatchRune(r)
	}
}
func (p *streamPattern) starts(r rune) bool {
	for _, pc := range p.first {
		if streamRune(&p.program.Inst[pc], r) {
			return true
		}
	}
	return false
}
func (p *streamPattern) couldStart(r rune) bool {
	if r >= 0 && r < 128 {
		return p.ascii[r]
	}
	return p.starts(r)
}

type streamThread struct {
	pc    uint32
	start int64
}
type streamNFA struct {
	seeds, ready, stack []streamThread
	seen                []uint64
	best                []int64
	generation          uint64
}

// advance resolves assertions with real lookahead, then consumes next. States
// coalesce only by program counter and earliest original start, never by text.
func (n *streamNFA) advance(p *syntax.Prog, prev, next rune, pos int64, search bool) (active, matched int64) {
	active, matched = -1, -1
	if len(n.seen) != len(p.Inst) {
		n.seen = make([]uint64, len(p.Inst))
		n.best = make([]int64, len(p.Inst))
	}
	n.generation++
	if n.generation == 0 {
		clear(n.seen)
		n.generation = 1
	}
	n.stack = append(n.stack[:0], n.seeds...)
	n.ready = n.ready[:0]
	if search {
		n.stack = append(n.stack, streamThread{uint32(p.Start), pos})
	}
	cond := syntax.EmptyOpContext(prev, next)
	for len(n.stack) > 0 {
		t := n.stack[len(n.stack)-1]
		n.stack = n.stack[:len(n.stack)-1]
		if t.pc == 0 || n.seen[t.pc] == n.generation && n.best[t.pc] <= t.start {
			continue
		}
		n.seen[t.pc], n.best[t.pc] = n.generation, t.start
		i := &p.Inst[t.pc]
		switch i.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			n.stack = append(n.stack, streamThread{i.Out, t.start}, streamThread{i.Arg, t.start})
		case syntax.InstCapture, syntax.InstNop:
			n.stack = append(n.stack, streamThread{i.Out, t.start})
		case syntax.InstEmptyWidth:
			if syntax.EmptyOp(i.Arg)&cond == syntax.EmptyOp(i.Arg) {
				n.stack = append(n.stack, streamThread{i.Out, t.start})
			}
		case syntax.InstMatch:
			if matched < 0 || t.start < matched {
				matched = t.start
			}
		case syntax.InstRune, syntax.InstRune1, syntax.InstRuneAny, syntax.InstRuneAnyNotNL:
			if streamRune(i, next) {
				n.ready = append(n.ready, streamThread{i.Out, t.start})
				if active < 0 || t.start < active {
					active = t.start
				}
			}
		}
	}
	// Closure traversal order can visit a later start before the earlier
	// equivalent thread. Keep one output per counter as well as one best
	// closure state; otherwise repeated prefixes can retain duplicate seeds.
	n.generation++
	if n.generation == 0 {
		clear(n.seen)
		n.generation = 1
	}
	out := n.ready[:0]
	for _, t := range n.ready {
		if n.seen[t.pc] != n.generation {
			n.seen[t.pc], n.best[t.pc] = n.generation, int64(len(out))
			out = append(out, t)
		} else if slot := int(n.best[t.pc]); t.start < out[slot].start {
			out[slot].start = t.start
		}
	}
	n.ready = out
	n.seeds, n.ready = n.ready, n.seeds[:0]
	return
}
func (n *streamNFA) reset() { n.seeds = n.seeds[:0]; n.ready = n.ready[:0]; n.stack = n.stack[:0] }
func (n *streamNFA) keep(start int64) {
	out := n.seeds[:0]
	for _, t := range n.seeds {
		if t.start == start {
			out = append(out, t)
		}
	}
	n.seeds = out
}
func (n streamNFA) clone() streamNFA {
	return streamNFA{seeds: append([]streamThread(nil), n.seeds...)}
}

type streamMatcher struct {
	pattern       *streamPattern
	pending       []byte
	nfa           streamNFA
	pos, start    int64
	prev, before  rune
	accepted, end int64
	drain         byte // field, credential lexeme, or incomplete-header logical record
	recovery      streamRecovery
	finished      bool
}

func newStreamMatcher(pattern *streamPattern) streamMatcher {
	return streamMatcher{pattern: pattern, prev: -1, before: -1, accepted: -1, end: -1}
}
func (m *streamMatcher) clone() streamMatcher {
	c := *m
	c.pending = append([]byte(nil), m.pending...)
	c.nfa = m.nfa.clone()
	c.recovery = m.recovery.clone()
	return c
}
func (m *streamMatcher) clear() { clear(m.pending); *m = streamMatcher{} }
func (m *streamMatcher) resetAt(pos int64, prev rune) {
	clear(m.pending)
	m.pending = m.pending[:0]
	m.nfa.reset()
	m.pos, m.start, m.prev, m.before = pos, pos, prev, prev
	m.accepted, m.end = -1, -1
	m.drain = 0
	m.recovery = streamRecovery{}
}

func (m *streamMatcher) feed(ctx context.Context, text string, final bool, plain func(string) error, match func(string, bool) error) error {
	if plain == nil || match == nil {
		return errStreamMatch
	}
	return m.feedSpans(ctx, text, final, func(s string, _ int64) error { return plain(s) }, func(s string, forced bool, _ int64) error { return match(s, forced) })
}

func (m *streamMatcher) feedSpans(ctx context.Context, text string, final bool, plain func(string, int64) error, match func(string, bool, int64) error) error {
	if ctx == nil || m.pattern == nil || m.finished || plain == nil || match == nil || !utf8.ValidString(text) {
		return errStreamMatch
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.pos < 0 || int64(len(text)) > math.MaxInt64-m.pos {
		return ErrStreamPositionOverflow
	}
	var output []byte
	var outputStart int64
	defer func() { clear(output) }()
	flush := func() error {
		if len(output) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := plain(string(output), outputStart); err != nil {
			return err
		}
		clear(output)
		output = output[:0]
		return nil
	}
	emit := func(s string, start int64) error {
		if len(output) > 0 && outputStart+int64(len(output)) != start {
			if err := flush(); err != nil {
				return err
			}
		}
		for len(s) > 0 {
			space := streamForwardBytes - len(output)
			take := min(space, len(s))
			if take < len(s) {
				for take > 0 && !utf8.RuneStart(s[take]) {
					take--
				}
			}
			if take == 0 {
				if err := flush(); err != nil {
					return err
				}
				continue
			}
			if len(output) == 0 {
				outputStart = start
			}
			output = append(output, s[:take]...)
			start += int64(take)
			s = s[take:]
			if len(output) == streamForwardBytes {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		return nil
	}
	matched := func(s string, forced bool, start int64) error {
		if err := flush(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return match(s, forced, start)
	}
	rest := text
	var resume []string
	for steps := 0; ; steps++ {
		if steps&255 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if rest == "" && len(resume) > 0 {
			rest = resume[len(resume)-1]
			resume = resume[:len(resume)-1]
			continue
		}
		if rest == "" && !final {
			break
		}
		// Ordinary runs have no pending assertion or candidate to cross them.
		if rest != "" && m.drain == 0 && len(m.nfa.seeds) == 0 && len(m.pending) == 0 && m.accepted < 0 {
			cut := 0
			last := m.prev
			for cut < len(rest) {
				r, size := utf8.DecodeRuneInString(rest[cut:])
				if m.pattern.couldStart(r) {
					break
				}
				last = r
				cut += size
			}
			if cut > 0 {
				if err := emit(rest[:cut], m.pos); err != nil {
					return err
				}
				m.pos += int64(cut)
				m.start = m.pos
				m.prev, m.before = last, last
				rest = rest[cut:]
				continue
			}
		}
		next, size := rune(-1), 0
		if rest != "" {
			next, size = utf8.DecodeRuneInString(rest)
		}
		piece := ""
		if size > 0 {
			piece = rest[:size]
		}
		replay, retry, err := m.step(ctx, next, piece, emit, matched)
		if err != nil {
			return err
		}
		if retry {
			if replay != "" {
				if rest != "" {
					resume = append(resume, rest)
				}
				rest = replay
			}
			continue
		}
		if size == 0 {
			m.finished = true
			break
		}
		rest = rest[size:]
	}
	return flush()
}

func (m *streamMatcher) step(ctx context.Context, next rune, piece string, plain func(string, int64) error, match func(string, bool, int64) error) (string, bool, error) {
	if m.drain != 0 {
		return m.drainStep(next, piece)
	}
	active, found := m.nfa.advance(m.pattern.program, m.prev, next, m.pos, true)
	if found >= 0 && (m.accepted < 0 || found <= m.accepted) {
		m.accepted, m.end = found, m.pos
	}
	// Real EOF ends viability, but does not transfer an earlier opened
	// field to a later accepted alternative (such as a URL in its value).
	if next < 0 && len(m.pending) > 0 && m.pattern.opened != nil && m.pattern.opened.Match(m.pending) && (m.accepted < 0 || m.accepted > m.start) {
		if err := match(string(m.pending), true, m.start); err != nil {
			return "", false, err
		}
		m.resetAt(m.pos, m.prev)
		return "", false, nil
	}
	stable := m.accepted >= 0 && (active < 0 || active > m.accepted || m.pattern.lazy && active >= m.accepted)
	if stable {
		a, b := int(m.accepted-m.start), int(m.end-m.start)
		if a < 0 || b <= a || b > len(m.pending) {
			return "", false, errStreamMatch
		}
		if err := plain(string(m.pending[:a]), m.start); err != nil {
			return "", false, err
		}
		if err := match(string(m.pending[a:b]), false, m.accepted); err != nil {
			return "", false, err
		}
		replay := string(m.pending[b:])
		prev, _ := utf8.DecodeLastRune(m.pending[:b])
		end := m.end
		m.resetAt(end, prev)
		return replay, true, nil
	}
	if next < 0 {
		if len(m.pending) > 0 {
			if m.pattern.opened != nil && m.pattern.opened.Match(m.pending) {
				if err := match(string(m.pending), true, m.start); err != nil {
					return "", false, err
				}
			} else {
				if err := plain(string(m.pending), m.start); err != nil {
					return "", false, err
				}
			}
		}
		m.resetAt(m.pos, m.prev)
		return "", false, nil
	}
	keep := active
	if keep < 0 || m.accepted >= 0 && m.accepted < keep {
		keep = m.accepted
	}
	drop := len(m.pending)
	if keep >= 0 {
		drop = int(keep - m.start)
	}
	if drop < 0 || drop > len(m.pending) {
		return "", false, errStreamMatch
	}
	if drop > 0 {
		if err := plain(string(m.pending[:drop]), m.start); err != nil {
			return "", false, err
		}
		prev, _ := utf8.DecodeLastRune(m.pending[:drop])
		left := copy(m.pending, m.pending[drop:])
		clear(m.pending[left:])
		m.pending = m.pending[:left]
		m.start += int64(drop)
		m.before = prev
	}
	if keep < 0 {
		if err := plain(piece, m.pos); err != nil {
			return "", false, err
		}
		m.pos += int64(len(piece))
		m.start = m.pos
		m.prev, m.before = next, next
		return "", false, nil
	}
	if len(m.pending)+len(piece) > streamPendingBytes {
		mode := byte(3)
		if m.pattern.credential {
			mode = 2
		} else if m.accepted == m.start || m.pattern.opened != nil && m.pattern.opened.Match(m.pending) {
			mode = 1
		}
		if mode != 2 {
			if err := m.recovery.seed(ctx, m.pending); err != nil {
				return "", false, err
			}
		}
		if err := match(string(m.pending), true, m.start); err != nil {
			return "", false, err
		}
		clear(m.pending)
		m.pending = m.pending[:0]
		m.nfa.keep(m.start)
		m.drain = mode
		// advance already consumed the lookahead in the NFA. Raw text did not.
		if mode == 2 && !credentialRune(next) || mode == 3 && m.recovery.take(next) {
			m.resetAt(m.pos, m.prev)
			return "", true, nil
		}
		if mode == 1 {
			m.recovery.take(next)
		}
		m.pos += int64(len(piece))
		m.prev = next
		return "", false, nil
	}
	m.pending = append(m.pending, piece...)
	m.pos += int64(len(piece))
	m.prev = next
	return "", false, nil
}

func credentialRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
}
func (m *streamMatcher) drainStep(next rune, piece string) (string, bool, error) {
	if next < 0 {
		m.resetAt(m.pos, m.prev)
		return "", false, nil
	}
	switch m.drain {
	case 2:
		if !credentialRune(next) {
			m.resetAt(m.pos, m.prev)
			return "", true, nil
		}
	case 3:
		if m.recovery.take(next) {
			m.resetAt(m.pos, m.prev)
			return "", true, nil
		}
	default:
		active, found := m.nfa.advance(m.pattern.program, m.prev, next, m.pos, false)
		if found >= 0 && (m.accepted < 0 || found <= m.accepted) {
			m.accepted, m.end = found, m.pos
		}
		if m.accepted >= 0 && (active < 0 || active > m.accepted || m.pattern.lazy && active >= m.accepted) {
			m.resetAt(m.pos, m.prev)
			return "", true, nil
		}
		boundary := m.recovery.take(next)
		if active < 0 && m.accepted < 0 {
			m.drain = 3
			if boundary {
				m.resetAt(m.pos, m.prev)
				return "", true, nil
			}
		}
	}
	m.pos += int64(len(piece))
	m.prev = next
	return "", false, nil
}
