package privacy

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// StreamVersion is used only by the forthcoming ordered document format.
// Version-1 observations continue to use Redact and their original validation.
const StreamVersion = 2
const MaxStreamInputBytes = 64 << 10

var ErrStreamInput = errors.New("privacy: invalid stream input")
var ErrStreamClosed = errors.New("privacy: stream closed")
var ErrStreamBusy = errors.New("privacy: stream operation in progress")
var ErrStreamOutput = errors.New("privacy: stream output not accepted")
var ErrStreamCountOverflow = errors.New("privacy: stream count overflow")

type streamRule struct {
	pattern *streamPattern
	kind    string
	label   *regexp.Regexp
}

var streamRules = makeStreamRules()

func makeStreamRules() []streamRule {
	json := regexp.MustCompile(strings.ReplaceAll(jsonLabel.String(), `\\.`, `\\(?s:.)`))
	quoted := regexp.MustCompile(strings.ReplaceAll(quotedLabel.String(), `\\.`, `\\(?s:.)`))
	// A blank before an opening quote is a separator, not a second secret.
	// Version 1 can backtrack its separator and mask that blank after the
	// quoted rule already masked the value, corrupting its presentation.
	plainLabel := regexp.MustCompile(`(?i)\b(password|secret|token|api[_ -]?key|cvv|cvc|security[_ -]?code)\b([ \t]*[:=][ \t]*)([^\r\n,;&"' \t][^\r\n,;&"']*)`)
	opaque := func(re *regexp.Regexp) *regexp.Regexp {
		return regexp.MustCompile("(?:" + webURL.String() + ")|(?:" + re.String() + ")")
	}
	// Recognition already checked the original preceding rune. Requiring a
	// fresh word boundary at substring EOF would lose Unicode-folded starts.
	capture := func(re *regexp.Regexp) *regexp.Regexp {
		return regexp.MustCompile(strings.Replace(re.String(), `\b`, `^`, 1))
	}
	return []streamRule{
		{newStreamPattern(pem, true, nil), "PRIVATE_KEY", nil},
		{newStreamPattern(auth, false, nil), "AUTHORIZATION", nil},
		{newStreamPattern(webURL, false, nil), "URL", nil},
		{newStreamPattern(opaque(json), false, streamJSONOpened), "JSON", json},
		{newStreamPattern(opaque(quoted), false, streamQuotedOpened), "QUOTED", capture(quoted)},
		{newStreamPattern(opaque(plainLabel), false, nil), "LABEL", capture(plainLabel)},
		{newStreamPattern(token, false, nil), "TOKEN", nil},
		{newStreamPattern(ssn, false, nil), "SSN", nil},
		{newStreamPattern(card, false, nil), "CARD", nil},
		{newStreamPattern(iban, false, nil), "IBAN", nil},
	}
}

// Stream is single-owner capture state. It retains bounded pending candidates,
// never the complete document. Sink success consumes the entire output batch;
// sink error must consume nothing, so the input can be retried safely.
type Stream struct {
	stages                       []streamMatcher
	counts                       map[string]int
	origins                      []streamLineage
	outputPosition               int64
	closed, busy, abortRequested bool
}

func NewStream() *Stream {
	s := &Stream{stages: make([]streamMatcher, len(streamRules)), counts: map[string]int{}}
	for i, rule := range streamRules {
		s.stages[i] = newStreamMatcher(rule.pattern)
	}
	return s
}
func (s *Stream) Write(ctx context.Context, input string, sink func(context.Context, string) error) error {
	return s.operation(ctx, input, false, sink)
}
func (s *Stream) Finish(ctx context.Context, sink func(context.Context, string) error) error {
	return s.operation(ctx, "", true, sink)
}
func (s *Stream) clearStates() {
	for i := range s.stages {
		s.stages[i].clear()
	}
	s.stages = nil
	s.origins = nil
}
func (s *Stream) Abort() {
	if s == nil {
		return
	}
	if s.busy {
		s.abortRequested = true
		s.closed = true
		return
	}
	s.clearStates()
	s.closed = true
}
func (s *Stream) Counts() []Count {
	if s == nil {
		return nil
	}
	return sortedCounts(s.counts)
}
func (s *Stream) clone() *Stream {
	trial := &Stream{stages: make([]streamMatcher, len(s.stages)), counts: make(map[string]int, len(s.counts)), outputPosition: s.outputPosition}
	for i := range s.stages {
		trial.stages[i] = s.stages[i].clone()
	}
	for rule, count := range s.counts {
		trial.counts[rule] = count
	}
	if s.origins != nil {
		trial.origins = make([]streamLineage, len(s.origins))
		for i := range s.origins {
			trial.origins[i] = s.origins[i].clone()
		}
	}
	return trial
}
func (s *Stream) operation(ctx context.Context, input string, final bool, sink func(context.Context, string) error) error {
	if sink == nil {
		return s.operationMapped(ctx, input, final, false, nil)
	}
	return s.operationMapped(ctx, input, final, false, func(ctx context.Context, text string, _ []OriginSpan) error { return sink(ctx, text) })
}
func (s *Stream) operationMapped(ctx context.Context, input string, final, commitEmpty bool, sink func(context.Context, string, []OriginSpan) error) error {
	if s == nil {
		return ErrStreamClosed
	}
	if s.busy {
		return ErrStreamBusy
	}
	if s.closed || len(s.stages) != len(streamRules) {
		return ErrStreamClosed
	}
	if ctx == nil || sink == nil || len(input) > MaxStreamInputBytes || !utf8.ValidString(input) || strings.ContainsRune(input, 0) {
		return ErrStreamInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.busy = true
	s.abortRequested = false
	defer func() {
		s.busy = false
		if s.abortRequested {
			s.clearStates()
			s.closed = true
		}
	}()
	trial := s.clone()
	defer trial.clearStates()
	var output string
	var origins []OriginSpan
	var delta map[string]int
	var err error
	if trial.origins != nil {
		output, origins, delta, err = trial.processMapped(ctx, input, final)
	} else {
		output, delta, err = trial.process(ctx, input, final)
	}
	if err != nil {
		return err
	}
	for rule, count := range delta {
		if count < 0 || trial.counts[rule] > math.MaxInt-count {
			return ErrStreamCountOverflow
		}
		trial.counts[rule] += count
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if output != "" || commitEmpty {
		if err := sink(ctx, output, origins); err != nil {
			if errors.Is(err, context.Canceled) {
				return context.Canceled
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return context.DeadlineExceeded
			}
			return ErrStreamOutput
		}
	}
	// Sink success is irrevocable even if its deadline just expired. Abort from
	// the callback commits accepted counts, then the deferred terminal cleanup.
	s.clearStates()
	s.stages = trial.stages
	trial.stages = nil
	s.origins = trial.origins
	trial.origins = nil
	s.outputPosition = trial.outputPosition
	s.counts = trial.counts
	s.closed = final || s.abortRequested
	if s.closed {
		s.clearStates()
	}
	return nil
}
func (s *Stream) process(ctx context.Context, input string, final bool) (string, map[string]int, error) {
	var out strings.Builder
	delta := map[string]int{}
	var send func(int, string) error
	feed := func(i int, text string, end bool) error {
		return s.stages[i].feed(ctx, text, end, func(plain string) error { return send(i+1, plain) }, func(raw string, forced bool) error {
			return send(i+1, streamReplacement(streamRules[i], raw, forced, delta))
		})
	}
	send = func(i int, text string) error {
		if text == "" {
			return nil
		}
		if i == len(s.stages) {
			out.WriteString(text)
			return nil
		}
		return feed(i, text, false)
	}
	if final {
		for i := range s.stages {
			if err := feed(i, "", true); err != nil {
				return "", nil, err
			}
		}
	} else if err := send(0, input); err != nil {
		return "", nil, err
	}
	return out.String(), delta, nil
}
func streamReplacement(rule streamRule, raw string, forced bool, counts map[string]int) string {
	urlMatch := false
	if rule.label != nil {
		span := webURL.FindStringIndex(raw)
		urlMatch = span != nil && span[0] == 0
	}
	if forced {
		name := rule.kind
		switch name {
		case "URL":
			name = "MALFORMED_URL"
		case "JSON", "QUOTED", "LABEL":
			if urlMatch {
				name = "MALFORMED_URL"
			} else {
				name = "SECRET"
				for _, head := range []*regexp.Regexp{streamJSONOpened, streamQuotedOpened, streamSecretName} {
					if parts := head.FindStringSubmatch(raw); len(parts) > 1 {
						name = labelRule(parts[1])
						break
					}
				}
			}
		}
		counts[name]++
		return placeholder(name)
	}
	if urlMatch {
		return raw
	}
	switch rule.kind {
	case "URL":
		return redactURLMatch(raw, counts)
	case "AUTHORIZATION":
		return redactAuthorizationMatch(raw, counts)
	case "JSON":
		return redactQuotedMatch(raw, rule.label, true, counts)
	case "QUOTED":
		return redactQuotedMatch(raw, rule.label, false, counts)
	case "LABEL":
		return redactLabelMatchUsing(raw, rule.label, counts)
	case "SSN":
		if !validSSN(raw) {
			return raw
		}
	case "CARD":
		if !validCard(raw) {
			return raw
		}
	case "IBAN":
		if !validIBAN(raw) {
			return raw
		}
	}
	counts[rule.kind]++
	return placeholder(rule.kind)
}

var streamSecretName = regexp.MustCompile(`(?i)^"?(password|secret|token|api[_ -]?key|cvv|cvc|security[_ -]?code)\b`)
