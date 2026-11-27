package assistant

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/search"
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
	Kind                Kind
	WholeDocument       bool        // explicit request to analyze all parts of a captured source
	EventTime           bool        // question dates describe an event in evidence, not when evidence was observed
	EventSubjectTerms   []string    // inherited event topic, kept separately from replaceable date cues
	NumberedSubjects    [][2]string // topic word and adjacent number; lexical support, not a semantic identity
	TechnicalSubjects   []string    // exact code/agent labels; their component words cannot replace the label
	TechnicalComparison bool        // independently compare named subjects; inherited only by genuine continuations
	Scope               Scope
	AppIDs              []string // canonical app ids the question names
	AppNames            []string // display names for AppIDs, same order
	Domains             []string // web domains the question names
	Terms               []string // normalised topic terms, stop words removed, sorted
	Entity              string   // subject of a KindEntity question
	Question            string   // normalised question text
	Literal             bool     // Search-only quoted/code terms require lexical matches.
	LiteralTerms        []string // Mandatory topics restored after scope/task grammar.
	FilterQuestion      string   // Search-only grammar with literal topics masked.
	MatchOnly           bool     // ambiguity probe: require a topic match, never recent-source fallback
	ReferenceQuestion   string   // original retrieval question retained through grounded continuations
	SubjectFollowup     bool     // factual continuation may retrieve new matches for the inherited named subject
	ContextFollowup     bool     // attribute continuation reads within an authenticated cited source context
	Conversation        bool     // recap of the saved conversation with the person named in Entity
}

// Turn is one earlier exchange, kept so follow-ups can inherit scope.
type Turn struct {
	Question      string
	Answer        string
	Plan          Plan
	General       bool              // explicitly generated without personal-memory evidence
	SourceIDs     []string          // cited source handles; reload current evidence before using a private answer
	SourceDigests map[string]string // changed derived evidence invalidates old answer prose, even if its handle is stable
}

// Layer orders evidence from broad, cheap summaries to raw captures.
type Layer int

const (
	LayerSource Layer = iota // a raw captured observation
	LayerHour                // an hourly chronicle summary
	LayerDay                 // a stored recap of one whole local day
	LayerWeek                // a stored recap of one whole ISO week
	LayerDigest              // a Library digest
	LayerMonth               // appended to preserve existing stored enum values
	LayerYear
	LayerCompaction // independent generated summaries covering one saved source
)

// Diagnosis explains an empty or thin retrieval so the answer can say why.
type Diagnosis struct {
	MissingTechnicalSubjects   []string      // requested subjects not covered by selected source passages
	VoiceRequested             bool          // the question asks for spoken content, not browser playback controls
	VoiceAssociationUnverified bool          // a recent transcript does not establish the requested website/video identity
	VoiceRecallLimited         bool          // bounded transcript selection omitted other saved rows or sessions
	RecapSampled               bool          // time-distributed retrieval omitted other saved candidates
	RollupsDirty               int           // day or week recaps in scope that are waiting to be regenerated
	TaskSearchLimited          bool          // bounded current-task recall omitted candidates
	LibraryRecallLimited       bool          // bounded subject-name lookup omitted candidate Library entries
	SemanticIssue              string        // closed retrieval coverage: disabled, loading, unavailable, limited
	IndexingPending            bool          // saved sources still have exact-search work queued
	CapturePaused              bool          // capture is off or paused right now
	PausedSince                time.Time     // when it was paused, if known
	AppKnown                   bool          // the named app has been captured before
	SourcesInScope             int           // captured sources inside the time scope
	ModelFree                  bool          // the answer was produced without a model
	CaptureIssue               string        // closed capture-health category; never source/error text
	Chronological              bool          // a recap over more than 24 hours is packed oldest first
	TermsRelaxed               bool          // an item actually packed for the answer matches only some of the question's terms (set after packing)
	ScopeWidened               time.Duration // a recency question found nothing in its window and looked this far back instead
}

// Retrieval is what a planned retrieval returns.
type Retrieval struct {
	Targets          []TargetRef
	Evidence         []Evidence
	AlternateQueries []string // bounded model-ranked search phrases for one fallback retrieval
	Diagnosis        Diagnosis
}

// PlanRetriever answers a Plan instead of a bare question. A retriever that
// implements it gets structured scope and app filters; otherwise the service
// falls back to Retrieve with the question text.
type PlanRetriever interface {
	RetrievePlan(context.Context, Plan) (Retrieval, error)
}

// ExpandedPlanRetriever performs a single scope-preserving retry using
// alternate phrases already produced while ranking the original candidates.
type ExpandedPlanRetriever interface {
	RetrievePlanWithQueries(context.Context, Plan, []string) (Retrieval, error)
}

// SourceRetriever reloads the original answer's cited evidence from current
// storage. Missing/deleted handles must never return cached answer text.
type SourceRetriever interface {
	RetrieveSources(context.Context, Plan, []string) (Retrieval, error)
}

// AppLister exposes the apps and sites a retriever has seen so the planner can
// recognise them in questions.
type AppLister interface {
	KnownApps(context.Context) ([]KnownApp, error)
}

// PlanningClock keeps relative-date parsing aligned with the source and task
// retrieval clock, including its timezone at a local-day boundary.
type PlanningClock interface {
	AskTime() time.Time
}

// DirectAnswerer answers some plans without a model (time spent, capture
// listings). ok is false when the plan needs evidence and a model.
type DirectAnswerer interface {
	AnswerDirect(context.Context, Plan) (answer DirectAnswer, ok bool, err error)
}

// DirectAnswer carries the same inspectable source evidence as a generated
// answer, while keeping deterministic capture listings independent of a model.
type DirectAnswer struct {
	Answer
	Sources         []Evidence
	Targets         []TargetRef
	Publication     PublicationGuard `json:"-"`
	CoverageLimited bool
	IndexingPending bool
}

// PublicationGuard installs already-loaded state only while the read remains
// current. Callbacks must not perform IO or call back into the retriever.
type PublicationGuard interface {
	Publish(func()) bool
}

// TargetRef is an app-owned action handle returned by a local read. Models
// receive only its short Label, never choose IDs or revisions themselves.
type TargetRef struct {
	Kind     string
	ID       string
	Revision uint64
	Label    string
	SourceID string
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
	return planQuestion(question, now, apps, false)
}

func planQuestion(question string, now time.Time, apps []KnownApp, searchMode bool) (Plan, error) {
	question, err := normalizeQuestion(question)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Kind: KindGeneral, Question: question}
	plan.TechnicalSubjects = technicalSubjects(question)
	plan.TechnicalComparison = !searchMode && len(plan.TechnicalSubjects) > 1 && technicalComparisonQuestion(question, plan.TechnicalSubjects)
	var literal []string
	if searchMode {
		question, literal, err = searchGrammar(question)
		if err != nil {
			return Plan{}, err
		}
		plan.Literal = len(literal) > 0
		plan.LiteralTerms = literal
		plan.FilterQuestion = question
	}
	if len(plan.TechnicalSubjects) > 0 {
		literal, err = search.NormalizeTokens(strings.Join(append(slices.Clone(literal), plan.TechnicalSubjects...), " "), search.MaxTerms)
		if err != nil {
			return Plan{}, err
		}
		plan.Literal, plan.LiteralTerms = true, literal
	}
	question = maskTechnicalSubjects(question, plan.TechnicalSubjects)
	ws := splitWords(question)
	scopeQuestion := question
	if !searchMode {
		if grammar, _, maskErr := searchGrammar(question); maskErr == nil {
			scopeQuestion = grammar
		}
	}
	dutch := dutchRecallGrammar(question, ws)
	// Domains and quoted subjects are content, not observation-time phrases,
	// in both native Ask and Search.
	scopeWords := searchScopeWords(scopeQuestion, ws)
	scopeWords, plan.EventTime = timeQuestionScope(scopeWords)
	if dutch.active {
		scopeWords, plan.EventTime = dutch.scope, dutch.event
		scopeWords = searchScopeWords(scopeQuestion, scopeWords)
	}
	allScopeWords := ws
	if dutch.active {
		allScopeWords = dutchCalendarView(ws)
	}
	scope, scopeWordsUsed := detectScope(scopeWords, now, allScopeWords)
	if dutch.active {
		scopeWordsUsed = append(scopeWordsUsed, dutchScopeTerms(ws, scopeWordsUsed, scope)...)
	}
	ids, names, domains, consumed := matchApps(question, ws, apps)
	more, consumed := findDomains(question, consumed)
	for _, d := range more {
		if !containsString(domains, d) {
			domains = append(domains, d)
		}
	}
	if len(scopeWordsUsed) > 0 {
		if consumed == nil {
			consumed = make(map[string]struct{})
		}
		for _, used := range scopeWordsUsed {
			consumed[used] = struct{}{}
		}
	}
	entity := detectEntity(question, ws)
	if entity == "" {
		entity = detectOutstandingSubject(ws)
	}
	var conversation conversationRequest
	if !searchMode {
		conversation = detectConversationRecap(question, ws, consumed)
	}
	var speaker string
	if !searchMode && entity == "" && !conversation.ok {
		var verb string
		if speaker, verb = detectSpeaker(question, ws, consumed); speaker != "" && slices.Contains(neutralSpeechVerbs, verb) {
			consumed = withWords(consumed, verb)
		}
	}
	named := len(ids) > 0 || len(domains) > 0
	retrievalEnd := max(retrievalCommandEnd(ws), recordLocationEnd(ws, searchMode && len(literal) > 0))
	// A conversation recap names a person, not a document, even though it
	// uses the words "recap" and "conversation".
	plan.WholeDocument = !searchMode && !conversation.ok && detectWholeDocumentRequest(ws)
	memory := len(plan.TechnicalSubjects) > 0 || dutch.memory || plan.WholeDocument || searchMode || retrievalEnd > 0 || QuestionSeeksPersonalMemory(question) || !scope.IsZero() ||
		entity != "" && IsOutstandingQuestion(question) ||
		startsWith(ws, "what", "or", "who", "is") ||
		startsWith(ws, "what", "do", "you", "know") || startsWith(ws, "what", "do", "you", "remember") ||
		startsWith(ws, "do", "you", "know") || startsWith(ws, "do", "you", "remember")
	// Scope and apps are recorded even for general questions so a short
	// follow-up ("and in Slack?") can be completed from them.
	plan.Scope = scope
	plan.AppIDs, plan.AppNames, plan.Domains = ids, names, domains
	if plan.WholeDocument {
		if consumed == nil {
			consumed = make(map[string]struct{})
		}
		for _, word := range ws {
			if isWholeDocumentGrammarWord(word.s) {
				consumed[word.s] = struct{}{}
			}
		}
	}
	// Short general continuations still carry a newly named topic. Without
	// these terms, "And Mina?" looks identical to a scope-only continuation.
	if !searchMode && IsFollowUp(question) {
		plan.Terms, err = planTerms(question, consumed, !scope.IsZero() && !plan.EventTime)
		if err != nil {
			return Plan{}, err
		}
	}
	if !searchMode && !plan.WholeDocument && IsCreationRequest(question) && !creationNeedsMemory(question) {
		return plan, nil
	}
	if !memory && (!named || !detectTimeKind(ws) && !detectCaptureKind(ws)) {
		return plan, nil
	}
	if !searchMode && scope.Label == ScopeJustNow {
		consumed = withWords(consumed, recencyGrammarWords...)
	}
	if !searchMode && storageQuestion(ws) {
		consumed = withWords(consumed, storageGrammarWords...)
	}
	if retrievalEnd > 0 {
		plan.MatchOnly = true
		for _, word := range ws[:retrievalEnd] {
			consumed = withWords(consumed, word.s)
		}
	}
	if plan.EventTime || dutch.memory {
		plan.MatchOnly = true
	}
	if len(plan.TechnicalSubjects) > 0 {
		plan.MatchOnly = true
	}
	if strings.TrimSpace(question) != "" {
		plan.Terms, err = planTerms(question, consumed, !scope.IsZero() && !plan.EventTime)
	}
	if err != nil {
		return Plan{}, err
	}
	if !searchMode && len(plan.TechnicalSubjects) > 0 {
		plan.Terms = slices.Clone(literal)
	} else if searchMode {
		plan.Terms, err = searchPlanTerms(plan.Terms, literal, ws)
		if err != nil {
			return Plan{}, err
		}
	}
	switch {
	case plan.WholeDocument:
		plan.Kind = KindLookup
	case detectTimeKind(ws):
		plan.Kind = KindTime
		// Time-query grammar is not an activity subject. Keep project/topic
		// names so unsupported classification cannot become an all-app total.
		grammar := withWords(nil, "during", "using")
		if hasSeq(ws, "total", "screen", "time") || hasSeq(ws, "total", "time") {
			grammar = withWords(grammar, "total")
		}
		if hasSeq(ws, "screen", "time") {
			grammar = withWords(grammar, "screen")
		}
		terms := plan.Terms[:0]
		for _, term := range plan.Terms {
			if _, consumed := grammar[term]; !consumed {
				terms = append(terms, term)
			}
		}
		plan.Terms = terms
	case detectCaptureKind(ws):
		plan.Kind = KindCapture
	case scope.Label == ScopeJustNow && len(plan.Terms) == 0:
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
	if len(plan.TechnicalSubjects) > 0 && plan.Kind != KindTime {
		plan.Kind = KindLookup
	}
	if conversation.ok {
		plan.Kind, plan.Entity, plan.Conversation, plan.MatchOnly = KindLookup, conversation.name, true, true
		plan.Terms, err = planTerms(conversation.name, nil, false)
		if err != nil || len(plan.Terms) == 0 {
			return Plan{}, ErrContext
		}
		if plan.Scope.IsZero() {
			plan.Scope = Scope{Start: now.AddDate(0, 0, -ConversationDefaultDays), End: now, Label: "last " + strconv.Itoa(ConversationDefaultDays) + " days"}
		}
		return plan, nil
	}
	if speaker != "" && plan.Kind == KindLookup {
		plan.Entity = speaker
	}
	if IsOutstandingQuestion(question) {
		plan.Kind = KindEntity
		plan.Entity = entity
		plan.MatchOnly = true
		// In project-work questions the final name is the subject, even when
		// it also aliases a captured website (for example, CraftedSignal and
		// accounts.craftedsignal.io). Do not turn the subject into an app or
		// domain filter; that would hide saved tasks and project knowledge.
		plan.AppIDs, plan.AppNames, plan.Domains = nil, nil, nil
		plan.Terms, err = planTerms(entity, nil, false)
		if err != nil {
			return Plan{}, err
		}
	}
	if plan.EventTime {
		plan.EventSubjectTerms = eventSubjectTerms(plan)
	}
	// Search's local question has literal text masked. An unrelated quoted
	// qualifier must not disable cues from the remaining numbered topic.
	plan.NumberedSubjects = numberedSubjects(question, plan.Terms)
	return plan, nil
}

// IsCreationRequest identifies an explicit request for an answer or draft.
// Merely using "me", "email", or a future date in a draft does not make it a
// request to retrieve the user's history.
func IsCreationRequest(question string) bool {
	q := strings.ToLower(strings.TrimSpace(question))
	q = strings.TrimSpace(strings.TrimPrefix(q, "please "))
	for _, prefix := range []string{"write ", "draft ", "compose ", "rewrite ", "rephrase ", "translate ", "proofread ", "explain ", "compare ", "debug ", "help me write ", "help me understand "} {
		if strings.HasPrefix(q, prefix) {
			return true
		}
	}
	return false
}
func creationNeedsMemory(question string) bool {
	q := strings.ToLower(question)
	// Pasted text belongs to the current request. Historical words inside it
	// ("rewrite this: I worked yesterday") do not trigger background retrieval.
	if at := strings.Index(q, ":"); at >= 0 {
		q = q[:at]
	}
	for _, phrase := range []string{"what i ", "what we ", "what you remember", "my notes", "our notes", "my saved", "my last ", "my previous ", "i read", "i saw", "i worked", "we discussed", "we decided", "from memory", "my day", "my activity", "my captures", "my history"} {
		if strings.Contains(q, phrase) {
			return true
		}
	}
	return false
}

// Inherit fills a follow-up's missing scope and app from the previous turn's
// plan. A question that names its own window or app keeps it.
func (p Plan) Inherit(prev Plan) Plan {
	p, _ = eventDateContinuation(p, prev)
	if p.Kind == KindGeneral {
		return p
	}
	if p.Scope.IsZero() && !p.EventTime && !prev.Scope.IsZero() {
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
	if len(p.TechnicalSubjects) == 0 && p.Entity == "" && (technicalContinuation(p) || technicalComparisonContinuation(p, prev)) && len(prev.TechnicalSubjects) > 0 {
		p.TechnicalSubjects = slices.Clone(prev.TechnicalSubjects)
		p.TechnicalComparison = prev.TechnicalComparison && !technicalRelationshipQuestion(p.Question, p.TechnicalSubjects)
		p.Terms, p.LiteralTerms = slices.Clone(prev.Terms), slices.Clone(prev.LiteralTerms)
		p.Literal, p.MatchOnly = true, true
		if p.Kind != KindTime {
			p.Kind = KindLookup
		}
	}
	return p
}

// SearchQuery is the exact-search query string for a plan's terms.
func (p Plan) SearchQuery() string { return strings.Join(p.Terms, " ") }
