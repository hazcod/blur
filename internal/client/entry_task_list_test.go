package client

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

type taskListModelSpy struct{ calls atomic.Int64 }

func (m *taskListModelSpy) CountInputTokens(context.Context, string, string) (int, error) {
	m.calls.Add(1)
	return 1, nil
}

type delayedTaskListStore struct {
	*durable.Store
	entered, release         chan struct{}
	enteredOnce, releaseOnce sync.Once
}

func (s *delayedTaskListStore) ListTasks(ctx context.Context, request tasks.PageRequest) (tasks.Page, error) {
	page, err := s.Store.ListTasks(ctx, request)
	s.enteredOnce.Do(func() { close(s.entered); <-s.release })
	return page, err
}

func TestComposerShowsTaskReadProgressWhileTheLocalListLoads(t *testing.T) {
	for _, surface := range []string{"dashboard", "popup"} {
		t.Run(surface, func(t *testing.T) {
			c, store, _ := todayTaskClient(t)
			delayed := &delayedTaskListStore{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
			c.data = delayed
			app := &meetingOperationTestApp{posted: make(chan func(), 8)}
			d := &desktop{app: app, client: c, page: "Ask", askQuestion: "todos?", quickInput: "todos?"}
			if surface == "dashboard" {
				d.submitMemoryQuery()
			} else {
				d.submitQuickPanelInput()
			}
			select {
			case <-delayed.entered:
			case <-time.After(3 * time.Second):
				delayed.releaseOnce.Do(func() { close(delayed.release) })
				t.Fatal("task request did not reach its local task read")
			}
			if d.status != "Loading your tasks…" {
				delayed.releaseOnce.Do(func() { close(delayed.release) })
				t.Fatalf("%s showed misleading request progress: %q", surface, d.status)
			}
			delayed.releaseOnce.Do(func() { close(delayed.release) })
			finishMemoryEntry(t, app, d)
		})
	}
}

func TestDelayedTaskListPublicationPreservesNewDraftAndRejectsRetiredEpoch(t *testing.T) {
	for _, test := range []struct {
		name, draft string
		retire      bool
	}{
		{"same draft", "tasks?", false},
		{"newer draft", "new unrelated note draft", false},
		{"retired result", "new draft after memory changed", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, store, _ := todayTaskClient(t)
			if _, err := c.CreateTask(context.Background(), "Synthetic local result", nil); err != nil {
				t.Fatal(err)
			}
			delayed := &delayedTaskListStore{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
			t.Cleanup(func() { delayed.releaseOnce.Do(func() { close(delayed.release) }) })
			c.data = delayed
			type outcome struct {
				resolved EntryResolution
				err      error
			}
			completed := make(chan outcome, 1)
			go func() {
				r, e := c.ResolveEntry(context.Background(), "tasks?", EntryState{}, "UTC")
				completed <- outcome{r, e}
			}()
			select {
			case <-delayed.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("local task read did not enter")
			}
			d := &desktop{app: &quickDashboardApp{}, client: c, quickInput: test.draft}
			d.quickUI.historyHidden = true
			delayed.releaseOnce.Do(func() { close(delayed.release) })
			var result outcome
			select {
			case result = <-completed:
			case <-time.After(3 * time.Second):
				t.Fatal("local task read did not finish")
			}
			if result.err != nil {
				t.Fatal(result.err)
			}
			if test.retire {
				c.NotifyMemoryChanging()
				c.NotifyMemoryStable()
			}
			d.dispatchEntryResolution("tasks?", result.resolved, "UTC")
			want := test.draft
			if want == "tasks?" {
				want = ""
			}
			if d.quickInput != want {
				t.Fatalf("local result erased newer draft: got %q want %q", d.quickInput, want)
			}
			if test.retire {
				if d.askResult != nil || len(d.quickConversation) != 0 {
					t.Fatal("retired local result republished saved memory")
				}
			} else if d.askResult == nil || len(d.quickConversation) != 1 {
				t.Fatal("current local answer was lost")
			}
			if test.draft != "tasks?" && !d.quickUI.historyHidden {
				t.Fatal("new draft unexpectedly revealed old history")
			}
		})
	}
}
func (m *taskListModelSpy) Generate(context.Context, assistant.PackedContext, int) (assistant.ModelAnswer, error) {
	m.calls.Add(1)
	return assistant.ModelAnswer{}, errors.New("task-list fixture must not generate")
}
func (m *taskListModelSpy) GenerateGeneralPrompt(context.Context, string, string, int) (string, error) {
	m.calls.Add(1)
	return `{"kind":"ask","mode":"memory"}`, nil
}
func (*taskListModelSpy) CloseIdleConnections() {}

func TestShortTaskListsResolveAndAnswerLocallyWithoutModelCalls(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	spy := &taskListModelSpy{}
	ai := &localAIController{service: notes.NewService(store, nil, nil), settings: LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}, askModel: spy}
	c := NewClient(store).WithLocalAI(ai)
	defer c.Close()
	defer ai.Close()
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	c.askNow = func() time.Time { return now }
	service, err := reminders.NewService(store, &clientNoticePort{}, reminders.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	c.reminders = service
	created, err := c.CreateTask(ctx, "Review the deployment PR", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"tasks?", "todo?", "todos?", "TASKS?!", "to‑dos？", "my to-do list!", "please show my tasks", "Could you show my tasks, please?", "What’s on my plate?", "What do I need to do?", "What do I still need to get done?", "What should I finish?"} {
		t.Run(input, func(t *testing.T) {
			resolved, err := c.ResolveEntry(ctx, input, EntryState{}, "UTC")
			if err != nil || resolved.Action.Kind != intent.TaskInspect || resolved.Answer == nil || !resolved.Answer.Direct || !strings.Contains(resolved.Answer.Answer.Text, created.Item.Title) {
				t.Fatalf("shared main/popout entry missed local task list: %+v err=%v", resolved, err)
			}
			answer, err := c.AskMemory(ctx, input)
			if err != nil || !answer.Direct || !strings.Contains(answer.Answer.Text, created.Item.Title) {
				t.Fatalf("Ask did not use the same local task list: %+v err=%v", answer, err)
			}
			if calls := spy.calls.Load(); calls != 0 {
				t.Fatalf("deterministic task list spent %d model/tokenizer calls", calls)
			}
		})
	}
	for _, input := range []string{"what integrity emails were we talking about?", "What was I doing in Herdr?", "Who is Mira?", "How do I implement a cache?", "What are the differences between Go channels and mutexes?"} {
		t.Run(input, func(t *testing.T) {
			resolved, err := c.ResolveEntry(ctx, input, EntryState{}, "UTC")
			if err != nil || resolved.Action.Kind != intent.Ask || resolved.Action.Text != input || spy.calls.Load() != 0 {
				t.Fatalf("clear read question repeated selected-model intent inference: %+v calls=%d err=%v", resolved, spy.calls.Load(), err)
			}
		})
	}
}
