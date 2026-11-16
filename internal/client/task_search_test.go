package client

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

func TestSearchFindsLiveExplicitTaskAndKeepsCurrentRevision(t *testing.T) {
	c, _, now := todayTaskClient(t)
	ctx := context.Background()
	created, err := c.CreateTask(ctx, "Drycleaners pickup", &tasks.Reminder{DueAt: now.AddDate(0, 0, 4), Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := c.SearchMemoryPageContext(ctx, "drycleaners")
	if err != nil || len(page.Hits) != 1 || page.Hits[0].AppID != "blur.tasks" || !strings.Contains(page.Hits[0].Text, "Current state: open") || !strings.Contains(page.Hits[0].Text, "16 Nov 2026") {
		t.Fatal("explicit saved task missing from Search", page, err)
	}
	oldSource := page.Hits[0].SourceID
	item := created.Item
	item.Title, item.State = "Drycleaners pickup completed", tasks.Completed
	updated, err := c.UpdateTask(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	page, err = c.SearchMemoryPageContext(ctx, "drycleaners")
	if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID == oldSource || !strings.Contains(page.Hits[0].Text, "Current state: completed") || !strings.Contains(page.Hits[0].Text, item.Title) {
		t.Fatal("Search returned stale task copy", page, err)
	}
	if err := c.DeleteTask(ctx, updated.Item.ID, updated.Item.Revision); err != nil {
		t.Fatal(err)
	}
	page, err = c.SearchMemoryPageContext(ctx, "drycleaners")
	if err != nil || len(page.Hits) != 0 {
		t.Fatal("removed task returned from Search", page, err)
	}
}

func TestAskRecallsMatchingTaskWithRevisionBoundTargetWithoutModel(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	ctx := context.Background()
	created, err := c.CreateTask(ctx, "Drycleaners pickup", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTask(ctx, "Review compiler benchmarks", nil); err != nil {
		t.Fatal(err)
	}
	result, err := c.AskMemory(ctx, "What do you remember about drycleaners?")
	if err != nil || result.General || !strings.Contains(result.Answer.Text, "Drycleaners pickup") || len(result.Sources) != 1 || result.Sources[0].Origin != assistant.OriginTask || len(result.Targets) != 1 || result.Targets[0].ID != created.Item.ID || result.Targets[0].Revision != created.Item.Revision {
		t.Fatal("Ask omitted live task recall or target", result, err)
	}
	item := created.Item
	item.Title = "Drycleaners collect the coats"
	updated, err := c.UpdateTask(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	result, err = c.AskMemory(ctx, "What do you remember about drycleaners?")
	if err != nil || !strings.Contains(result.Answer.Text, item.Title) || strings.Contains(result.Answer.Text, "Drycleaners pickup") || len(result.Targets) != 1 || result.Targets[0].Revision != updated.Item.Revision {
		t.Fatal("Ask returned old task revision", result, err)
	}
	if err := c.DeleteTask(ctx, updated.Item.ID, updated.Item.Revision); err != nil {
		t.Fatal(err)
	}
	result, err = c.AskMemory(ctx, "What do you remember about drycleaners?")
	if err != nil || len(result.Sources) != 0 || len(result.Targets) != 0 {
		t.Fatal("Ask returned removed task", result, err)
	}
}

func TestTopicalTaskQuestionDoesNotReturnEveryOpenTask(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	ctx := context.Background()
	created, err := c.CreateTask(ctx, "Drycleaners pickup", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTask(ctx, "Review compiler benchmarks", nil); err != nil {
		t.Fatal(err)
	}
	result, err := c.AskMemory(ctx, "What is my drycleaners task?")
	if err != nil || len(result.Sources) != 1 || len(result.Targets) != 1 || result.Targets[0].ID != created.Item.ID || strings.Contains(result.Answer.Text, "compiler") {
		t.Fatal("topical task question became full list", result, err)
	}
	plan, err := assistant.PlanQuestion("What is my compiler reminder?", c.askClock(), nil)
	if err != nil {
		t.Fatal(err)
	}
	personal, err := c.PersonalAnswerContext(ctx, plan)
	if err != nil || len(personal.Facts) != 0 || len(personal.Targets) != 0 {
		t.Fatal("topical task question added all tasks as personal context", personal, err)
	}
}

type taskRecallEmbedder struct {
	question        string
	beforeDocuments func()
	score           float32
	failDocuments   bool
}

func (e *taskRecallEmbedder) Identity(context.Context) (semantic.Model, error) {
	return semantic.Model{Name: "task-fixture", Digest: strings.Repeat("a", 64), Dimensions: 2, Format: semantic.FormatPlain}, nil
}
func (e *taskRecallEmbedder) EmbedQuery(_ context.Context, _ semantic.Model, question string) ([]float32, error) {
	e.question = question
	return []float32{1, 0}, nil
}
func (e *taskRecallEmbedder) EmbedDocuments(_ context.Context, _ semantic.Model, texts []string) ([][]float32, error) {
	if e.failDocuments {
		return nil, semantic.ErrUnavailable
	}
	if e.beforeDocuments != nil {
		hook := e.beforeDocuments
		e.beforeDocuments = nil
		hook()
	}
	vectors := make([][]float32, len(texts))
	for i, text := range texts {
		score := float32(.1)
		if strings.Contains(text, "Drycleanres") {
			score = e.score
		}
		vectors[i] = []float32{score, float32(math.Sqrt(1 - float64(score*score)))}
	}
	return vectors, nil
}
func withTaskSemanticFixture(t *testing.T, c *Client, s *durable.Store, e *taskRecallEmbedder) {
	t.Helper()
	service, err := semantic.NewService(s, s, e, semantic.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.memoryIndex = &memoryIndexController{settings: DefaultMemoryIndexSettings(), service: service}
	t.Cleanup(service.Close)
}

func TestTaskMeaningRecallUsesWholePhraseAndLiveCurrentRecord(t *testing.T) {
	c, s, now := todayTaskClient(t)
	ctx := context.Background()
	created, err := c.CreateTask(ctx, "Drycleanres on monday", &tasks.Reminder{DueAt: now.AddDate(0, 0, 4), Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTask(ctx, "Review Monday compiler benchmarks", nil); err != nil {
		t.Fatal(err)
	}
	e := &taskRecallEmbedder{score: .8}
	withTaskSemanticFixture(t, c, s, e)
	phrase := "pick up my clothes"
	page, err := c.SearchMemoryPageContext(ctx, phrase)
	if err != nil || len(page.Hits) != 1 || page.Hits[0].Task == nil || page.Hits[0].Task.ID != created.Item.ID || e.question != phrase {
		t.Fatal("meaning recall failed for whole phrase", page, e.question, err)
	}
	question := "When do I pick up my clothes?"
	result, err := c.AskMemory(ctx, question)
	if err != nil || len(result.Sources) != 1 || len(result.Targets) != 1 || result.Targets[0].ID != created.Item.ID || !strings.Contains(result.Sources[0].Text, "16 Nov 2026") || e.question != question {
		t.Fatal("Ask did not recall task through full-question semantics", result, e.question, err)
	}
	item := created.Item
	item.Title = "Different current task title"
	e.beforeDocuments = func() {
		if _, err := c.UpdateTask(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	page, err = c.SearchMemoryPageContext(ctx, phrase)
	if err != nil || len(page.Hits) != 0 {
		t.Fatal("in-flight title edit published an old semantic match", page, err)
	}
}

func TestWeakTaskSemanticMatchDoesNotTurnGeneralQuestionIntoPersonalRecall(t *testing.T) {
	c, s, _ := todayTaskClient(t)
	if _, err := c.CreateTask(context.Background(), "Drycleanres on monday", nil); err != nil {
		t.Fatal(err)
	}
	withTaskSemanticFixture(t, c, s, &taskRecallEmbedder{score: .4})
	g := &namedSubjectAnswerGenerator{}
	result, err := assistant.NewService(c, g, integrationTokenCounter{}, assistant.ContextTokenLimit).Ask(context.Background(), "What is SQLite?")
	if err != nil || !result.General || len(result.Sources) != 0 || len(result.Targets) != 0 || g.generalCalls != 1 {
		t.Fatal("weak task match hijacked general question", result, err)
	}
}

// Opt-in quality check uses the real installed local embedding model. It keeps
// all task text in a disposable encrypted store and never reads user memory.
func TestLocalTaskMeaningRecallThroughEncryptedClient(t *testing.T) {
	if os.Getenv("BLUR_EMBEDDING_INTEGRATION") != "1" {
		t.Skip("requires local embeddinggemma")
	}
	c, s, now := todayTaskClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	created, err := c.CreateTask(ctx, "Drycleanres on monday", &tasks.Reminder{DueAt: now.AddDate(0, 0, 4), Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"Book a dentist appointment", "Pay the mortgage", "Buy milk", "Review Monday compiler benchmarks"} {
		if _, err := c.CreateTask(ctx, title, nil); err != nil {
			t.Fatal(err)
		}
	}
	c.memoryIndex = newMemoryIndexController(filepath.Join(t.TempDir(), "memory-index.json"), s, s, semantic.Options{CanRead: func() bool { return !c.MemoryIsChanging() }})
	c.addMemoryChangingHook(c.memoryIndex.Invalidate)
	t.Cleanup(c.memoryIndex.Close)
	waitMemoryIndex(ctx, t, c, 0)
	for _, phrase := range []string{"drycleaning", "drycleaners", "pick up my clothes", "collect laundry", "cleaning my suit"} {
		page, err := c.SearchMemoryPageContext(ctx, phrase)
		if err != nil {
			t.Fatal(phrase, err)
		}
		found := false
		for rank, hit := range page.Hits {
			if hit.Task != nil && hit.Task.ID == created.Item.ID {
				found = true
				t.Logf("%q found saved drycleaning task at rank %d", phrase, rank+1)
				break
			}
		}
		if !found {
			t.Fatalf("real semantic Search did not find task for %q: %+v", phrase, page)
		}
	}
	result, err := c.AskMemory(ctx, "When do I pick up my clothes?")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, target := range result.Targets {
		if target.ID == created.Item.ID && target.Revision == created.Item.Revision {
			found = true
		}
	}
	if !found || !strings.Contains(result.Answer.Text, "16 Nov 2026") {
		t.Fatalf("real Ask omitted task schedule or current action handle: %+v", result)
	}
	item := created.Item
	item.Title = "Drycleaners pickup completed"
	item.State = tasks.Completed
	updated, err := c.UpdateTask(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	result, err = c.AskMemory(ctx, "What do you remember about my drycleaning?")
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, source := range result.Sources {
		if strings.Contains(source.Text, "Saved task: Drycleaners pickup completed") && strings.Contains(source.Text, "Current state: completed") {
			found = true
		}
	}
	if !found {
		t.Fatal("real Ask returned stale task record", result)
	}
	if err := c.DeleteTask(ctx, updated.Item.ID, updated.Item.Revision); err != nil {
		t.Fatal(err)
	}
	waitMemoryIndex(ctx, t, c, 0)
	page, err := c.SearchMemoryPageContext(ctx, "pick up my clothes")
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range page.Hits {
		if hit.Task != nil && hit.Task.ID == created.Item.ID {
			t.Fatal("deleted task survived real semantic recall", hit)
		}
	}
}

func TestTaskRecallReportsSemanticFailureAndCandidateLimit(t *testing.T) {
	c, s, now := todayTaskClient(t)
	ctx := context.Background()
	for index := 0; index < maxTaskSemanticCandidates+1; index++ {
		item := tasks.Item{ID: fmt.Sprintf("%032x", index+1), Revision: 1, Title: fmt.Sprintf("Unrelated task %d", index), State: tasks.Open, CreatedAt: now, UpdatedAt: now, UserAuthored: true}
		if err := s.PutTask(ctx, item, 0); err != nil {
			t.Fatal(err)
		}
	}
	e := &taskRecallEmbedder{score: .1}
	withTaskSemanticFixture(t, c, s, e)
	page, err := c.SearchMemoryPageContext(ctx, "collect my laundry")
	if err != nil || !page.HasMore {
		t.Fatal("bounded task scan was presented as complete", page, err)
	}
	result, err := c.AskMemory(ctx, "What do you remember about my laundry?")
	if err != nil || !strings.Contains(result.Answer.Text, "task search reached its limit") {
		t.Fatal("limited task search claimed memory absence", result, err)
	}
	e.failDocuments = true
	result, err = c.AskMemory(ctx, "What do you remember about my laundry?")
	if err != nil || !strings.Contains(result.Answer.Text, "Search by meaning is unavailable") {
		t.Fatal("semantic failure was presented as absent saved tasks", result, err)
	}
}

func TestSourceMeaningRecallPreservesOriginalQuestion(t *testing.T) {
	c, s, _ := todayTaskClient(t)
	added, err := c.AddNote("Drycleanres on monday")
	if err != nil {
		t.Fatal(err)
	}
	e := &taskRecallEmbedder{score: .8}
	withTaskSemanticFixture(t, c, s, e)
	for _, kind := range []assistant.Kind{assistant.KindLookup, assistant.KindEntity} {
		question := "When do I pick up my clothes?"
		retrieval, err := c.RetrievePlan(context.Background(), assistant.Plan{Kind: kind, Question: question, Terms: []string{"clothes"}, Entity: "clothes"})
		if err != nil || len(retrieval.Evidence) != 1 || retrieval.Evidence[0].SourceID != added.SourceID || e.question != question {
			t.Fatal("source semantics stripped the full phrase", kind, retrieval, e.question, err)
		}
	}
}

func TestWeakSourceSemanticMatchDoesNotHijackGeneralQuestion(t *testing.T) {
	c, s, _ := todayTaskClient(t)
	if _, err := c.AddNote("Drycleanres on monday"); err != nil {
		t.Fatal(err)
	}
	withTaskSemanticFixture(t, c, s, &taskRecallEmbedder{score: .4})
	g := &namedSubjectAnswerGenerator{}
	result, err := assistant.NewService(c, g, integrationTokenCounter{}, assistant.ContextTokenLimit).Ask(context.Background(), "What is SQLite?")
	if err != nil || !result.General || len(result.Sources) != 0 || g.generalCalls != 1 {
		t.Fatal("weak source semantic match hijacked public question", result, err)
	}
}
