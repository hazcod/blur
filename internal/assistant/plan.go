package assistant

import (
	"context"
	"strings"
	"time"
)

// Kind is what a question asks for. The planner decides it without a model so
// that structured answers (time spent, capture listings) never wait on one.
type Kind int

const (
	// KindGeneral is a question that does not refer to the user's own memory.
	KindGeneral Kind = iota
	// KindLookup finds specific captured content by topic terms.
	KindLookup
	// KindRecap summarises a time window (hour, day, week).
	KindRecap
	// KindCapture lists what was captured, usually from one app or site.
	KindCapture
	// KindTime asks how much time went where.
	KindTime
	// KindEntity asks who or what something is (library knowledge).
	KindEntity
)

// Scope is the time window a question refers to. Zero times mean unbounded.
type Scope struct {
	Start    time.Time
	End      time.Time
	Label    string // "today", "yesterday", "this morning", "last hour", "this week"
	Explicit bool   // the question named the window; false when inherited or default
}

// IsZero reports whether no window was found.
func (s Scope) IsZero() bool { return s.Start.IsZero() && s.End.IsZero() }

// KnownApp is an application or site the retriever has seen, with the names
// a person would use for it. The planner matches question words against it.
type KnownApp struct {
	ID      string   // canonical app id (bundle id) or domain
	Name    string   // display name, e.g. "WhatsApp"
	Aliases []string // lower-case words that mean this app, e.g. "whatsapp", "wa"
	Domain  bool     // ID is a web domain rather than an app id
}

// Plan is the model-free reading of a question.
type Plan struct {
	Kind     Kind
	Scope    Scope
	AppIDs   []string // canonical app ids the question names
	AppNames []string // display names for AppIDs, same order
	Domains  []string // web domains the question names
	Terms    []string // normalised topic terms, stop words removed, sorted
	Entity   string   // subject of a KindEntity question
	Question string   // normalised question text
}

// Turn is one earlier exchange, kept so follow-ups can inherit scope.
type Turn struct {
	Question string
	Answer   string
	Plan     Plan
}

// Layer orders evidence from broad, cheap summaries to raw captures.
type Layer int

const (
	LayerSource Layer = iota // a raw captured observation
	LayerHour                // an hourly chronicle summary
	LayerDigest              // a daily or weekly digest
)

// Diagnosis explains an empty or thin retrieval so the answer can say why.
type Diagnosis struct {
	CapturePaused  bool      // capture is off or paused right now
	PausedSince    time.Time // when it was paused, if known
	AppKnown       bool      // the named app has been captured before
	SourcesInScope int       // captured sources inside the time scope
	ModelFree      bool      // the answer was produced without a model
}

// Retrieval is what a planned retrieval returns.
type Retrieval struct {
	Evidence  []Evidence
	Diagnosis Diagnosis
}

// PlanRetriever answers a Plan instead of a bare question. A retriever that
// implements it gets structured scope and app filters; otherwise the service
// falls back to Retrieve with the question text.
type PlanRetriever interface {
	RetrievePlan(context.Context, Plan) (Retrieval, error)
}

// AppLister exposes the apps and sites a retriever has seen so the planner can
// recognise them in questions.
type AppLister interface {
	KnownApps(context.Context) ([]KnownApp, error)
}

// DirectAnswerer answers some plans without a model (time spent, capture
// listings). ok is false when the plan needs evidence and a model.
type DirectAnswerer interface {
	AnswerDirect(context.Context, Plan) (answer Answer, ok bool, err error)
}

// PlanQuestion reads a question without a model: product principle "the model
// last step, never first". It is deterministic and allocation-light. It
// detects the kind (first match wins: time, capture, recap, entity, then
// general versus lookup by memory intent), the time window in now.Location(),
// the apps and sites named (matched against apps plus a built-in table of
// common apps), and the remaining topic terms. Structured answers need no
// model; a model, when used, only receives a small byte-bounded labelled
// selection of evidence and never the whole memory.
func PlanQuestion(question string, now time.Time, apps []KnownApp) (Plan, error) {
	question, err := normalizeQuestion(question)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Kind: KindGeneral, Question: question}
	ws := splitWords(question)
	scope, numeral := detectScope(ws, now)
	ids, names, domains, consumed := matchApps(question, ws, apps)
	more, consumed := findDomains(question, consumed)
	for _, d := range more {
		if !containsString(domains, d) {
			domains = append(domains, d)
		}
	}
	if numeral != "" {
		if consumed == nil {
			consumed = make(map[string]struct{})
		}
		consumed[numeral] = struct{}{}
	}
	entity := detectEntity(question, ws)
	named := len(ids) > 0 || len(domains) > 0
	memory := QuestionSeeksPersonalMemory(question) || !scope.IsZero() ||
		startsWith(ws, "what", "do", "you", "know") || startsWith(ws, "what", "do", "you", "remember") ||
		startsWith(ws, "do", "you", "know") || startsWith(ws, "do", "you", "remember")
	// Scope and apps are recorded even for general questions so a short
	// follow-up ("and in Slack?") can be completed from them.
	plan.Scope = scope
	plan.AppIDs, plan.AppNames, plan.Domains = ids, names, domains
	if !memory && !(named && (detectTimeKind(ws) || detectCaptureKind(ws))) {
		return plan, nil
	}
	if plan.Terms, err = planTerms(question, consumed, !scope.IsZero()); err != nil {
		return Plan{}, err
	}
	switch {
	case detectTimeKind(ws):
		plan.Kind = KindTime
	case detectCaptureKind(ws):
		plan.Kind = KindCapture
	case hasSummaryWord(ws) || detectRecapPattern(ws):
		plan.Kind = KindRecap
	case entity != "":
		plan.Kind = KindEntity
		plan.Entity = entity
	case len(plan.Terms) == 0 && named:
		plan.Kind = KindCapture
	case len(plan.Terms) == 0 && !scope.IsZero():
		plan.Kind = KindRecap
	default:
		plan.Kind = KindLookup
	}
	return plan, nil
}

// Inherit fills a follow-up's missing scope and app from the previous turn's
// plan. A question that names its own window or app keeps it.
func (p Plan) Inherit(prev Plan) Plan {
	if p.Kind == KindGeneral {
		return p
	}
	if p.Scope.IsZero() && !prev.Scope.IsZero() {
		p.Scope = prev.Scope
		p.Scope.Explicit = false
	}
	if len(p.AppIDs) == 0 && len(prev.AppIDs) > 0 {
		p.AppIDs = append([]string(nil), prev.AppIDs...)
		p.AppNames = append([]string(nil), prev.AppNames...)
	}
	if len(p.Domains) == 0 && len(prev.Domains) > 0 {
		p.Domains = append([]string(nil), prev.Domains...)
	}
	return p
}

// SearchQuery is the exact-search query string for a plan's terms.
func (p Plan) SearchQuery() string { return strings.Join(p.Terms, " ") }
