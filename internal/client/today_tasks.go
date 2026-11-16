package client

import (
	"context"
	"slices"
	"time"

	"github.com/hazcod/blur/internal/assistant"

	"github.com/hazcod/blur/internal/tasks"
	"github.com/hazcod/blur/internal/today"
)

func (c *Client) TaskSourceEvidence(ctx context.Context, id string, revision uint64) ([]assistant.Evidence, error) {
	if c == nil || !tasks.ValidID(id) || revision == 0 {
		return nil, ErrInvalidRequest
	}
	operation, done, err := c.beginOperation(ctx, shortOperationTimeout)
	if err != nil {
		return nil, err
	}
	defer done()
	operation, cancel := context.WithCancel(operation)
	request, stable := c.registerAsk(cancel)
	if !stable {
		cancel()
		return nil, ErrMemoryChanging
	}
	defer func() { cancel(); c.unregisterAsk(request) }()
	item, err := c.Task(operation, id)
	if err != nil {
		return nil, err
	}
	if item.Revision != revision {
		return nil, tasks.ErrConflict
	}
	var result []assistant.Evidence
	for _, sourceID := range item.Sources() {
		source, err := c.ChronicleSource(operation, sourceID)
		if err != nil {
			return nil, err
		}
		result = append(result, assistant.Evidence{SourceID: sourceID, AppID: source.AppID, Domain: source.Domain, Text: source.Text, Partial: source.Partial})
	}
	return result, operation.Err()
}

func todayTaskCandidate(date string, todo today.Todo) tasks.TodayCandidate {
	return tasks.TodayCandidate{Date: date, ID: todo.ID, Text: todo.Text, SourceIDs: append([]string(nil), todo.SourceIDs...)}
}

func (c *Client) dismissTodayTaskAtEpoch(parent context.Context, accepted tasks.TodayCandidate, epoch uint64) error {
	if c == nil {
		return ErrNotConfigured
	}
	repository, ok := c.data.(tasks.TodayDismissRepository)
	if !ok {
		return ErrNotConfigured
	}
	if err := accepted.Validate(); err != nil {
		return err
	}
	ctx, done, err := c.beginOperation(parent, shortOperationTimeout)
	if err != nil {
		return err
	}
	defer done()
	ctx, cancel := context.WithCancel(ctx)
	id, stable := c.registerAskEpoch(cancel, &epoch)
	if !stable {
		cancel()
		return ErrMemoryChanging
	}
	defer func() { cancel(); c.unregisterAsk(id) }()
	err = c.CommitDerivedMemory(epoch, func() error { return repository.DismissTodayTask(ctx, accepted) })
	if err == nil {
		c.notifyTasksChanged()
	}
	return err
}

func (c *Client) confirmTodayTaskAtEpoch(parent context.Context, accepted tasks.TodayCandidate, epoch uint64) (tasks.Item, error) {
	if c == nil {
		return tasks.Item{}, ErrNotConfigured
	}
	repository, ok := c.data.(tasks.TodayRepository)
	if !ok {
		return tasks.Item{}, ErrNotConfigured
	}
	if err := accepted.Validate(); err != nil {
		return tasks.Item{}, err
	}
	ctx, done, err := c.beginOperation(parent, shortOperationTimeout)
	if err != nil {
		return tasks.Item{}, err
	}
	defer done()
	ctx, cancel := context.WithCancel(ctx)
	id, stable := c.registerAskEpoch(cancel, &epoch)
	if !stable {
		cancel()
		return tasks.Item{}, ErrMemoryChanging
	}
	defer func() { cancel(); c.unregisterAsk(id) }()
	item, err := repository.ConfirmTodayTask(ctx, accepted, c.askClock())
	if err == nil {
		c.notifyTasksChanged()
	}
	return item, err
}

// Legacy explicit confirmations may be imported, but a plain candidate never
// activates on a read. Receipts for removed tasks are retained in storage so
// opening Today cannot recreate them. The preview includes only current open
// tasks; the main view can also inspect completed/cancelled items.
func (c *Client) resolveTodayTasks(ctx context.Context, epoch uint64, out *TodayView) error {
	repository, ok := c.taskRepository()
	if !ok {
		return nil
	}
	active := make([]today.Todo, 0, len(out.Snapshot.ConfirmedTodos))
	seen := map[string]bool{}
	for _, todo := range out.Snapshot.ConfirmedTodos {
		var item tasks.Item
		if todo.TaskID == "" {
			var err error
			item, err = c.confirmTodayTaskAtEpoch(ctx, todayTaskCandidate(out.Date, todo), epoch)
			if err != nil {
				out.TaskIssues++
				out.UnimportedTasks = append(out.UnimportedTasks, todo)
				continue
			}
			todo.TaskID = item.ID
		} else {
			var found bool
			var err error
			item, found, err = repository.GetTask(ctx, todo.TaskID)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
		}
		if !slices.Equal(item.Sources(), todo.SourceIDs) {
			return ErrInvalidRequest
		}
		if seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		out.Tasks = append(out.Tasks, item)
		if item.State == tasks.Open {
			todo.Text = todayTaskText(item)
			active = append(active, todo)
		}
	}
	items, err := c.liveTasks(ctx)
	if err != nil {
		return err
	}
	zone, err := loadZone(out.Snapshot.Zone)
	if err != nil {
		zone = c.askClock().Location()
	}
	start, err := time.ParseInLocation("2006-01-02", out.Date, zone)
	if err != nil {
		return ErrInvalidRequest
	}
	end := start.AddDate(0, 0, 1)
	currentDay := c.askClock().In(zone).Format("2006-01-02") == out.Date
	for _, item := range items {
		if seen[item.ID] || !taskVisibleOnDay(item, start, end, currentDay) {
			continue
		}
		seen[item.ID] = true
		out.Tasks = append(out.Tasks, item)
		if item.State == tasks.Open {
			active = append(active, today.Todo{ID: item.ID, TaskID: item.ID, Text: todayTaskText(item), SourceIDs: item.Sources(), Confirmed: true})
		}
	}
	if !c.MemoryReadCurrent(epoch) {
		return ErrMemoryChanging
	}
	out.Snapshot.ConfirmedTodos = active
	return nil
}

func taskVisibleOnDay(item tasks.Item, start, end time.Time, currentDay bool) bool {
	if !item.CreatedAt.Before(start) && item.CreatedAt.Before(end) {
		return true
	}
	if item.State != tasks.Open || !item.CreatedAt.Before(end) {
		return false
	}
	if item.Reminder == nil {
		return currentDay
	}
	due := taskDue(item, start)
	return !due.IsZero() && due.Before(end) && (currentDay || !due.Before(start))
}

func todayTaskText(item tasks.Item) string {
	if item.Reminder == nil {
		return item.Title
	}
	reminder := *item.Reminder
	if item.Delivery.State == tasks.Scheduled && !item.Delivery.NextAt.IsZero() {
		reminder.Repeat, reminder.DueAt = tasks.Once, item.Delivery.NextAt
	}
	return item.Title + " — " + reminderScheduleCopy(reminder)
}
