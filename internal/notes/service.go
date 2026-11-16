// Package notes captures bounded user-authored evidence and turns it into
// bounded, source-linked proposals. Model output is never activated directly.
package notes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/model"
)

const (
	MaxNoteBytes        = capture.MaxObservationTextBytes
	MaxProposals        = 8
	MaxProposalBytes    = 512
	maxNoteExtractCalls = 128
)

var ErrInvalid = errors.New("notes: invalid note")

type Kind string

const (
	Task     Kind = "task"
	Reminder Kind = "reminder"
	FollowUp Kind = "follow_up"
	Question Kind = "question"
)

type Proposal struct {
	ID        string    `json:"id"`
	Kind      Kind      `json:"kind"`
	Text      string    `json:"text"`
	Due       string    `json:"due,omitempty"`
	SourceID  string    `json:"source_id"`
	CreatedAt time.Time `json:"created_at"`
	Confirmed bool      `json:"confirmed"`
}

type Repository interface {
	SaveNote(context.Context, capture.SanitizedObservation, time.Time) error
	SaveProposals(context.Context, string, []Proposal) error
}

// Extractor may use a configured local or explicitly authorized model. The
// note passed to it is already bounded, redacted, and stored locally.
type Extractor interface {
	Extract(context.Context, capture.SanitizedObservation) ([]Proposal, error)
}

type ProcessingState string

const (
	ProcessingDeterministic          ProcessingState = "deterministic"
	ProcessingModel                  ProcessingState = "model_processed"
	ProcessingUnavailable            ProcessingState = "model_unavailable"
	ProcessingInvalidOutput          ProcessingState = "invalid_output"
	ProcessingBudget                 ProcessingState = "model_budget_exceeded"
	ProcessingSuggestionsUnavailable ProcessingState = "suggestions_unavailable"
	ProcessingQueued                 ProcessingState = "model_queued"
)

type Result struct {
	SourceID   string          `json:"source_id"`
	CreatedAt  time.Time       `json:"created_at"`
	Proposals  []Proposal      `json:"proposals"`
	Processing ProcessingState `json:"processing"`
}

type Service struct {
	mu          sync.RWMutex
	repository  Repository
	extractor   Extractor
	modelCtx    context.Context
	cancelModel context.CancelFunc
	now         func() time.Time
}

func NewService(repository Repository, extractor Extractor, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	modelCtx, cancelModel := context.WithCancel(context.Background())
	return &Service{repository: repository, extractor: extractor, modelCtx: modelCtx, cancelModel: cancelModel, now: now}
}

// SetExtractor applies an explicit user model setting. Reconfiguration
// cancels in-flight extraction before the new setting takes effect.
func (s *Service) SetExtractor(extractor Extractor) {
	if s == nil {
		return
	}
	modelCtx, cancelModel := context.WithCancel(context.Background())
	s.mu.Lock()
	oldCancel, oldExtractor := s.cancelModel, s.extractor
	s.extractor, s.modelCtx, s.cancelModel = extractor, modelCtx, cancelModel
	s.mu.Unlock()
	if oldCancel != nil {
		oldCancel()
	}
	if closer, ok := oldExtractor.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (s *Service) AIConfigured() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.extractor != nil
}

func (s *Service) Add(ctx context.Context, text string) (Result, error) {
	return s.add(ctx, text, true)
}

// AddReceipt acknowledges encrypted source persistence and deterministic
// suggestions without invoking the configured extractor. It starts no work
// after returning. Callers may notify consent-gated background task/Library
// workers using the returned SourceID; Add remains the synchronous extraction API.
func (s *Service) AddReceipt(ctx context.Context, text string) (Result, error) {
	return s.add(ctx, text, false)
}

func (s *Service) add(ctx context.Context, text string, extract bool) (Result, error) {
	if s == nil || s.repository == nil || ctx == nil || ctx.Err() != nil || len(text) == 0 || len(text) > MaxNoteBytes || !utf8.ValidString(text) || strings.TrimSpace(text) == "" || strings.ContainsRune(text, 0) {
		return Result{}, ErrInvalid
	}
	at := s.now()
	if at.IsZero() {
		return Result{}, ErrInvalid
	}
	observation, err := capture.SanitizeObservation("sh.blur.notes", "local", text, MaxNoteBytes)
	if err != nil || observation.Truncated() {
		return Result{}, ErrInvalid
	}
	if documents, ok := s.repository.(interface {
		SaveNoteDocument(context.Context, capture.SanitizedObservation, time.Time) (capture.SanitizedObservation, error)
	}); ok {
		observation, err = documents.SaveNoteDocument(ctx, observation, at)
		if err != nil {
			return Result{}, err
		}
	} else if err := s.repository.SaveNote(ctx, observation, at); err != nil {
		return Result{}, err
	}
	result, generation, _ := s.prepare(ctx, observation, at, extract)
	if extract {
		// Cancellation alone cannot retire an uncooperative provider. Serialize
		// model-derived persistence with SetExtractor, preserving only local
		// deterministic suggestions if its generation has already changed.
		s.mu.RLock()
		defer s.mu.RUnlock()
		if result.Processing == ProcessingModel && (s.modelCtx != generation || generation.Err() != nil || ctx.Err() != nil) {
			result.Processing = ProcessingUnavailable
			result.Proposals, _ = deterministic(observation.Text(), observation.ContentID(), at)
		}
	}
	if len(result.Proposals) > 0 {
		if err := s.repository.SaveProposals(ctx, observation.ContentID(), result.Proposals); err != nil {
			result.Processing = ProcessingUnavailable
			if !extract {
				result.Processing = ProcessingSuggestionsUnavailable
			}
		}
	}
	return result, nil
}

func (s *Service) prepare(ctx context.Context, observation capture.SanitizedObservation, at time.Time, extract bool) (Result, context.Context, error) {
	var deferred error
	proposals, valid := deterministic(observation.Text(), observation.ContentID(), at)
	state := ProcessingDeterministic
	if !valid {
		proposals = nil
		state = ProcessingInvalidOutput
	}
	s.mu.RLock()
	extractor, modelCtx := s.extractor, s.modelCtx
	s.mu.RUnlock()
	if extract && extractor != nil && ctx.Err() == nil && modelCtx.Err() == nil {
		modelRequestCtx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(modelCtx, cancel)
		remainingCalls := maxNoteExtractCalls
		suggestions, modelErr := extractNoteSuggestions(modelRequestCtx, extractor, observation, &remainingCalls)
		stop()
		cancel()
		if modelErr != nil {
			if errors.Is(modelErr, model.ErrBackgroundPreempted) {
				deferred = modelErr
			}
			if errors.Is(modelErr, ErrModelBudget) {
				state = ProcessingBudget
			} else {
				state = ProcessingUnavailable
			}
		} else {
			var ok bool
			suggestions, ok = validateModelOutput(suggestions)
			if !ok {
				state = ProcessingInvalidOutput
			} else {
				state = ProcessingModel
				for _, proposal := range suggestions {
					if proposal.Kind == Question && !meetingContext(observation.Text()) {
						continue
					}
					proposal.SourceID = observation.ContentID()
					proposal.CreatedAt = at
					proposal.Confirmed = false
					proposal.ID = proposalID(proposal.SourceID, proposal.Kind, proposal.Text)
					proposals = appendUnique(proposals, proposal)
				}
			}
		}
	}
	if len(proposals) > MaxProposals {
		proposals = proposals[:MaxProposals]
	}
	return Result{SourceID: observation.ContentID(), CreatedAt: at, Proposals: proposals, Processing: state}, modelCtx, deferred
}

// extractNoteSuggestions retries a context-budget refusal by splitting the
// already-sanitized note at UTF-8/newline boundaries. The saved source is
// never replaced by these temporary model inputs; prepare() rebinds accepted
// proposals to the original source ID and timestamp.
func extractNoteSuggestions(ctx context.Context, extractor Extractor, observation capture.SanitizedObservation, remainingCalls *int) ([]Proposal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if remainingCalls == nil || *remainingCalls <= 0 {
		return nil, ErrModelBudget
	}
	if budgeted, ok := extractor.(interface{ MaxNoteInputBytes() int }); ok {
		maxInput := budgeted.MaxNoteInputBytes()
		if maxInput <= 0 || (len(observation.Text())+maxInput-1)/maxInput > *remainingCalls {
			return nil, ErrModelBudget
		}
	}
	proposals, err := extractor.Extract(ctx, observation)
	if !errors.Is(err, ErrModelBudget) {
		*remainingCalls--
	}
	if err == nil || !errors.Is(err, ErrModelBudget) {
		return proposals, err
	}
	leftText, rightText, ok := splitNoteText(observation.Text())
	if !ok {
		return nil, err
	}
	left, err := capture.SanitizeObservation(observation.AppID(), observation.Domain(), leftText, MaxNoteBytes)
	if err != nil {
		return nil, ErrInvalid
	}
	right, err := capture.SanitizeObservation(observation.AppID(), observation.Domain(), rightText, MaxNoteBytes)
	if err != nil {
		return nil, ErrInvalid
	}
	first, err := extractNoteSuggestions(ctx, extractor, left, remainingCalls)
	if err != nil {
		return nil, err
	}
	if len(first) >= MaxProposals {
		return first[:MaxProposals], nil
	}
	second, err := extractNoteSuggestions(ctx, extractor, right, remainingCalls)
	if err != nil {
		return nil, err
	}
	for _, proposal := range second {
		first = appendUnique(first, proposal)
		if len(first) == MaxProposals {
			break
		}
	}
	return first, nil
}

func splitNoteText(text string) (string, string, bool) {
	if len(text) < 2 {
		return "", "", false
	}
	middle := len(text) / 2
	for middle > 0 && middle < len(text) && !utf8.RuneStart(text[middle]) {
		middle--
	}
	if middle == 0 || middle >= len(text) {
		return "", "", false
	}
	minBoundary, maxBoundary := len(text)/4, len(text)*3/4
	leftNewline := strings.LastIndexByte(text[minBoundary:middle], '\n')
	if leftNewline >= 0 {
		middle = minBoundary + leftNewline + 1
	} else if relative := strings.IndexByte(text[middle:maxBoundary], '\n'); relative >= 0 {
		middle += relative + 1
	}
	for middle > 0 && middle < len(text) && !utf8.RuneStart(text[middle]) {
		middle--
	}
	if middle <= 0 || middle >= len(text) {
		return "", "", false
	}
	return text[:middle], text[middle:], true
}

func meetingContext(text string) bool {
	text = strings.ToLower(text)
	for _, cue := range []string{"meeting", "standup", "stand-up", "retrospective", "workshop", "interview", "1:1", "one-on-one", "sync with", "call with", "met with", "meeting with", "discussed with"} {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

func deterministic(text, sourceID string, at time.Time) ([]Proposal, bool) {
	var out []Proposal
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		var candidate string
		kind := Task
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "- [ ]"):
			candidate = strings.TrimSpace(strings.TrimPrefix(line, "- [ ]"))
		case strings.HasPrefix(lower, "todo:"):
			candidate = strings.TrimSpace(line[len("todo:"):])
		case strings.HasPrefix(lower, "reminder:"):
			kind = Reminder
			candidate = strings.TrimSpace(line[len("reminder:"):])
		case strings.HasPrefix(lower, "remind me to "):
			kind = Reminder
			candidate = strings.TrimSpace(line[len("remind me to "):])
		}
		if candidate == "" {
			continue
		}
		if len(candidate) > MaxProposalBytes || !validProposalText(candidate) {
			return nil, false
		}
		p := Proposal{Kind: kind, Text: candidate, SourceID: sourceID, CreatedAt: at}
		p.ID = proposalID(sourceID, p.Kind, p.Text)
		out = appendUnique(out, p)
		if len(out) >= MaxProposals {
			break
		}
	}
	return out, true
}

func validateModelOutput(proposals []Proposal) ([]Proposal, bool) {
	if len(proposals) > MaxProposals {
		return nil, false
	}
	out := make([]Proposal, 0, len(proposals))
	questions := 0
	for _, p := range proposals {
		if !validKind(p.Kind) || len(p.Text) > MaxProposalBytes || !validProposalText(p.Text) || len(p.Due) > 64 || strings.ContainsRune(p.Due, 0) {
			return nil, false
		}
		if p.Kind == Question {
			questions++
			if questions > 1 || p.Due != "" || !strings.HasSuffix(p.Text, "?") {
				return nil, false
			}
		}
		p.ID, p.SourceID, p.CreatedAt, p.Confirmed = "", "", time.Time{}, false
		out = append(out, p)
	}
	return out, true
}

func validKind(kind Kind) bool {
	return kind == Task || kind == Reminder || kind == FollowUp || kind == Question
}
func validProposalText(value string) bool {
	return len(value) > 0 && strings.TrimSpace(value) == value && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func appendUnique(existing []Proposal, p Proposal) []Proposal {
	for _, old := range existing {
		if old.Kind == p.Kind && strings.EqualFold(old.Text, p.Text) && old.Due == p.Due {
			return existing
		}
	}
	return append(existing, p)
}

func proposalID(sourceID string, kind Kind, text string) string {
	return sourceID + ":" + string(kind) + ":" + shortDigest(text)
}

func shortDigest(text string) string {
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:8])
}
