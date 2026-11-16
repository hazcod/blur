package privacy

import (
	"context"
	"regexp/syntax"
	"unicode"
)

var streamRecoveryBegin = streamProgram(`-----BEGIN (?:[A-Z0-9 ]* )?PRIVATE KEY-----`)
var streamRecoveryEnd = streamProgram(`-----END (?:[A-Z0-9 ]* )?PRIVATE KEY-----`)
var streamRecoveryJSON = streamProgram(`(?i)"(password|secret|token|api[_ -]?key|cvv|cvc|security[_ -]?code)"[ \t]*:[ \t]*"`)
var streamRecoveryQuoted = streamProgram(`(?i)\b(password|secret|token|api[_ -]?key|cvv|cvc|security[_ -]?code)\b[ \t]*[:=][ \t]*"`)
var streamRecoveryURL = streamProgram(`(?i)https?://[^\s<>"']`)

// Recovery models the earlier replacements in policy order. Ordinary quote
// parity is insufficient: an unrelated quote does not close a sensitive field,
// and quotes swallowed by Authorization cannot close a later quoted rule.
// All retained state is program-sized metadata, never a raw field body.
type streamRecovery struct {
	begin, end         streamNFA
	pos                int64
	prev               rune
	initialized, inKey bool
	auth               streamAuthRecovery
	json, quoted       streamQuoteRecovery
}

func (r *streamRecovery) clone() streamRecovery {
	c := *r
	c.begin = r.begin.clone()
	c.end = r.end.clone()
	c.json = r.json.clone()
	c.quoted = r.quoted.clone()
	return c
}
func (r *streamRecovery) seed(ctx context.Context, input []byte) error {
	*r = streamRecovery{prev: -1, initialized: true}
	count := 0
	for _, next := range string(input) {
		if count&255 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		r.take(next)
		count++
	}
	return nil
}
func (r *streamRecovery) take(next rune) bool {
	if !r.initialized {
		r.prev = -1
		r.initialized = true
	}
	if r.inKey {
		_, found := r.end.advance(streamRecoveryEnd, r.prev, next, r.pos, true)
		if found < 0 {
			r.prev = next
			r.pos++
			return false
		}
		r.inKey = false
		r.end.reset()
		r.begin.reset()
	}
	_, found := r.begin.advance(streamRecoveryBegin, r.prev, next, r.pos, true)
	if found >= 0 {
		r.inKey = true
		r.begin.reset()
		r.afterKey('[') // Fixed placeholders contain no quotes or viable suffix.
		r.end.advance(streamRecoveryEnd, r.prev, next, r.pos, true)
		r.prev = next
		r.pos++
		return false
	}
	r.afterKey(next)
	boundary := next == '\n' && !r.auth.carry() && !r.json.owned && !r.quoted.owned
	r.prev = next
	r.pos++
	return boundary
}
func (r *streamRecovery) afterKey(next rune) {
	visible, opaque := r.auth.take(next)
	if opaque {
		r.afterAuth('[')
	}
	if visible {
		r.afterAuth(next)
	}
}
func (r *streamRecovery) afterAuth(next rune) {
	visible, opaque := r.json.take(streamRecoveryJSON, next)
	if opaque {
		r.quoted.take(streamRecoveryQuoted, '[')
	}
	if visible {
		r.quoted.take(streamRecoveryQuoted, next)
	}
}

type streamQuoteRecovery struct {
	head, url                          streamNFA
	pos                                int64
	prev                               rune
	initialized, owned, escaped, inURL bool
}

func (q *streamQuoteRecovery) clone() streamQuoteRecovery {
	c := *q
	c.head = q.head.clone()
	c.url = q.url.clone()
	return c
}
func (q *streamQuoteRecovery) take(program *syntax.Prog, next rune) (visible, opaque bool) {
	if !q.initialized {
		q.prev = -1
		q.initialized = true
	}
	if !q.owned {
		if !q.inURL {
			_, found := q.url.advance(streamRecoveryURL, q.prev, next, q.pos, true)
			if found >= 0 {
				q.inURL = true
				q.url.reset()
				q.head.reset()
			}
		}
		if q.inURL {
			if !streamSpace(next) && next != '<' && next != '>' && next != '"' && next != '\'' {
				q.prev = next
				q.pos++
				return true, false
			}
			// The URL alternative owns only its body. Its delimiter can
			// begin a real sensitive header in the same rule pass.
			q.inURL = false
			q.head.reset()
			q.url.reset()
		}
		_, found := q.head.advance(program, q.prev, next, q.pos, true)
		if found >= 0 {
			q.owned = true
			q.head.reset()
			q.url.reset()
			opaque = true
		}
	}
	if q.owned {
		if q.escaped {
			q.escaped = false
		} else if next == '\\' {
			q.escaped = true
		} else if next == '"' {
			q.owned = false
			visible = true
		}
	} else {
		visible = true
	}
	q.prev = next
	q.pos++
	return
}

type streamAuthRecovery struct {
	word     int
	phase    byte
	previous rune
	body     bool
}

func (r *streamAuthRecovery) carry() bool { return r.phase != 0 }
func streamSpace(r rune) bool             { return r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == '\f' }
func streamWord(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_'
}
func foldRune(a, b rune) bool {
	if a == b {
		return true
	}
	for r := unicode.SimpleFold(a); r != a; r = unicode.SimpleFold(r) {
		if r == b {
			return true
		}
	}
	return false
}
func (r *streamAuthRecovery) take(next rune) (visible, opaque bool) {
	if r.body {
		if next != '\r' && next != '\n' {
			return false, false
		}
		r.body = false
		r.word = 0
		r.phase = 0
		r.previous = ']'
	}
	if r.phase != 0 && !streamSpace(next) {
		if r.phase == 1 && next == ':' {
			r.phase = 2
		} else if r.phase == 2 {
			r.phase = 0
			r.word = 0
			r.body = true
			r.previous = ']'
			return false, true
		} else {
			r.phase = 0
		}
	}
	const name = "authorization"
	if r.word > 0 {
		if foldRune(rune(name[r.word]), next) {
			r.word++
			if r.word == len(name) {
				r.phase = 1
				r.word = 0
			}
		} else {
			r.word = 0
		}
	}
	if r.word == 0 && foldRune('a', next) && !streamWord(r.previous) {
		r.word = 1
	}
	r.previous = next
	return true, false
}
