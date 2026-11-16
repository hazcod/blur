package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/tasks"
)

func (c *Client) TaskChanges() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.taskChanges
}
func (c *Client) TaskOpened() <-chan string {
	if c == nil {
		return nil
	}
	return c.taskOpened
}
func (c *Client) notifyTasksChanged() {
	if c == nil || c.taskChanges == nil {
		return
	}
	select {
	case c.taskChanges <- struct{}{}:
	default:
	}
}
func (c *Client) TaskStatus() reminders.Status {
	if c == nil || c.reminders == nil {
		return reminders.Status{State: "unavailable", Authorization: reminders.Unsupported}
	}
	return c.reminders.Status()
}
func (c *Client) taskRepository() (tasks.Repository, bool) {
	if c == nil || c.data == nil {
		return nil, false
	}
	repository, ok := c.data.(tasks.Repository)
	return repository, ok
}

func (c *Client) TaskPage(ctx context.Context, after string, state tasks.State) (tasks.Page, error) {
	repository, ok := c.taskRepository()
	if !ok || ctx == nil {
		return tasks.Page{}, ErrNotConfigured
	}
	// State filters run before returning a page. Continue empty physical pages,
	// bounded per request; callers can still browse a continuation if necessary.
	for attempt := 0; attempt < 8; attempt++ {
		page, err := repository.ListTasks(ctx, tasks.PageRequest{Limit: tasks.MaxPageSize, After: after, State: state})
		if err != nil {
			return tasks.Page{}, err
		}
		if len(page.Items) > 0 || !page.HasMore || attempt == 7 {
			return page, nil
		}
		if page.Next == "" || page.Next == after {
			return tasks.Page{}, ErrInvalidRequest
		}
		after = page.Next
	}
	return tasks.Page{}, ErrInvalidRequest
}
func (c *Client) Task(ctx context.Context, id string) (tasks.Item, error) {
	repository, ok := c.taskRepository()
	if !ok || ctx == nil {
		return tasks.Item{}, ErrNotConfigured
	}
	item, found, err := repository.GetTask(ctx, id)
	if err != nil {
		return tasks.Item{}, err
	}
	if !found {
		return tasks.Item{}, ErrInvalidRequest
	}
	return item, nil
}

func (c *Client) beginTaskOperation(parent context.Context) (context.Context, func(), error) {
	return c.beginTaskOperationEpoch(parent, nil)
}
func (c *Client) beginTaskOperationEpoch(parent context.Context, epoch *uint64) (context.Context, func(), error) {
	if c == nil || c.reminders == nil {
		return nil, nil, ErrNotConfigured
	}
	operation, done, err := c.beginOperation(parent, 90*time.Second)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(operation)
	id, stable := c.registerAskEpoch(cancel, epoch)
	if !stable {
		cancel()
		done()
		return nil, nil, ErrMemoryChanging
	}
	return ctx, func() { c.unregisterAsk(id); cancel(); done() }, nil
}

func (c *Client) CreateTask(parent context.Context, title string, reminder *tasks.Reminder) (reminders.Result, error) {
	return c.createTask(parent, title, reminder, "")
}
func newTaskRequestID() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", ErrNotConfigured
	}
	return hex.EncodeToString(nonce[:]), nil
}
func (c *Client) createTask(parent context.Context, title string, reminder *tasks.Reminder, requestID string) (reminders.Result, error) {
	return c.createTaskWithEpoch(parent, title, reminder, requestID, nil)
}
func (c *Client) createTaskAtEpoch(parent context.Context, title string, reminder *tasks.Reminder, requestID string, epoch uint64) (reminders.Result, error) {
	return c.createTaskWithEpoch(parent, title, reminder, requestID, &epoch)
}
func (c *Client) createTaskWithEpoch(parent context.Context, title string, reminder *tasks.Reminder, requestID string, epoch *uint64) (reminders.Result, error) {
	ctx, done, err := c.beginTaskOperationEpoch(parent, epoch)
	if err != nil {
		return reminders.Result{}, err
	}
	defer done()
	var result reminders.Result
	if requestID != "" {
		result, err = c.reminders.Create(ctx, strings.TrimSpace(title), reminder, "", reminder != nil, requestID)
	} else {
		result, err = c.reminders.Create(ctx, strings.TrimSpace(title), reminder, "", reminder != nil)
	}
	if err == nil {
		c.notifyTasksChanged()
	}
	return result, err
}
func (c *Client) UpdateTask(parent context.Context, item tasks.Item) (reminders.Result, error) {
	return c.updateTaskWithEpoch(parent, item, nil)
}
func (c *Client) updateTaskWithEpoch(parent context.Context, item tasks.Item, epoch *uint64) (reminders.Result, error) {
	ctx, done, err := c.beginTaskOperationEpoch(parent, epoch)
	if err != nil {
		return reminders.Result{}, err
	}
	defer done()
	result, err := c.reminders.Update(ctx, item, item.Reminder != nil)
	if err == nil {
		c.notifyTasksChanged()
	}
	return result, err
}
func (c *Client) DeleteTask(parent context.Context, id string, revision uint64) error {
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return ErrMemoryChanging
	}
	return c.deleteTaskAtEpoch(parent, id, revision, epoch)
}
func (c *Client) deleteTaskAtEpoch(parent context.Context, id string, revision, epoch uint64) error {
	if c == nil || c.reminders == nil {
		return ErrNotConfigured
	}
	ctx, done, err := c.beginOperation(parent, 90*time.Second)
	if err != nil {
		return err
	}
	defer done()
	if !c.notifyMemoryChangingEpoch(&epoch) {
		return ErrMemoryChanging
	}
	defer c.NotifyMemoryStable()
	if err := c.reminders.Delete(ctx, id, revision); err != nil {
		return err
	}
	c.notifyTasksChanged()
	return nil
}
func (c *Client) RequestReminderAuthorization(parent context.Context) (reminders.Authorization, error) {
	ctx, done, err := c.beginTaskOperation(parent)
	if err != nil {
		return "", err
	}
	defer done()
	return c.reminders.RequestAuthorization(ctx)
}

func reminderZoneName() string {
	zone := localZoneName()
	if !tasks.ValidZone(zone) {
		return ""
	}
	return zone
}
