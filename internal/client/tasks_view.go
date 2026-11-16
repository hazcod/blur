package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/tasks"
)

func reminderMissingCopy(field string) string {
	switch field {
	case "title":
		return "What should I remind you to do? Add it after ‘to’."
	case "date":
		return "Which day should I use? Try ‘tomorrow at 09:00’ or ‘on 2027-01-15 at 09:00’."
	case "timezone":
		return "Which timezone should I use? Reply with an IANA name such as Europe/Brussels, or choose it in Tasks."
	case "recurrence":
		return "This reminder repeats. To change its recurring schedule, reply with a pattern such as ‘every Friday at 09:00’, or edit it in Tasks."
	case "future_time":
		return "That time has passed. Choose a future date and time."
	case "nonexistent_time":
		return "That local time does not exist because the clocks move forward. Choose another time."
	case "ambiguous_time":
		return "That local time occurs twice because the clocks move back. Append ‘first’ or ‘second’ after the time to choose its occurrence."
	default:
		return "At what time should I remind you? Try ‘tomorrow at 09:00’ or ‘in 10 minutes’."
	}
}
func reminderScheduleCopy(r tasks.Reminder) string {
	_, err := time.LoadLocation(r.Timezone)
	if err != nil {
		return "Timezone unavailable"
	}
	if r.Repeat == tasks.Daily {
		return fmt.Sprintf("Every day at %s", reminderClockCopy(r))
	}
	if r.Repeat == tasks.Weekly {
		return fmt.Sprintf("Every %s at %s", r.Weekday.String(), reminderClockCopy(r))
	}
	return reminderDateTimeCopy(r)
}
func taskDeliveryCopy(item tasks.Item) string {
	if item.State == tasks.Completed {
		return "Completed"
	}
	if item.State == tasks.Cancelled {
		return "Cancelled"
	}
	if item.Reminder == nil {
		return "Open task"
	}
	switch item.Delivery.State {
	case tasks.Scheduled:
		return "Scheduled with macOS"
	case tasks.Delivered:
		return "Observed in Notification Center"
	case tasks.Elapsed:
		return "Reminder time passed; delivery is unconfirmed"
	case tasks.Blocked:
		return "Saved; notifications are not allowed"
	case tasks.Failed:
		return "Saved; notification scheduling failed"
	case tasks.Scheduling:
		return "Saved; scheduling outcome is unconfirmed"
	default:
		return "Saved; waiting to schedule"
	}
}
func taskResultCopy(result reminders.Result) string {
	item := result.Item
	if item.State == tasks.Completed {
		return "Task completed. Its notifications have been canceled."
	}
	if item.State == tasks.Cancelled {
		return "Task canceled. Its notifications have been canceled."
	}
	if item.Reminder == nil {
		return "Task saved."
	}
	if result.ScheduleError != nil {
		if errors.Is(result.ScheduleError, reminders.ErrPermission) {
			return "Reminder saved, but notifications are not allowed. Enable them in Tasks to schedule it."
		}
		if errors.Is(result.ScheduleError, reminders.ErrUnsupported) {
			return "Task saved. Native reminders require the macOS app."
		}
		return "Reminder saved; scheduling could not be confirmed. Check Tasks before retrying."
	}
	if item.Delivery.State == tasks.Scheduled {
		reminder := *item.Reminder
		if reminder.Repeat == tasks.Once && !item.Delivery.NextAt.IsZero() {
			reminder.DueAt = item.Delivery.NextAt
		}
		return "Scheduled: " + reminderScheduleCopy(reminder) + "."
	}
	return taskDeliveryCopy(item) + "."
}

type taskSubmission struct {
	ID, Input, Title string
	Reminder         *tasks.Reminder
	Epoch            uint64
	DraftRevision    uint64
}

func (d *desktop) submitQuickTask(kind quickInputKind, text string) {
	d.submitQuickTaskDraft(kind, text, nil)
}
func (d *desktop) submitQuickTaskDraft(kind quickInputKind, text string, prepared *reminders.Draft) {
	d.submitQuickTaskDraftWithDraft(kind, text, prepared, d.quickInput, d.quickUI.draftRevision)
}

func (d *desktop) submitQuickTaskDraftWithDraft(kind quickInputKind, text string, prepared *reminders.Draft, input string, draftRevision uint64) {
	if d.client == nil || d.client.reminders == nil || d.loading || d.askLoading {
		d.status = "Tasks are unavailable right now. Your text is still here."
		d.app.RequestUpdate()
		return
	}
	title := strings.TrimSpace(text)
	var reminder *tasks.Reminder
	if prepared != nil {
		title, reminder = prepared.Title, prepared.Reminder
	}
	cached := d.quickTaskSubmission != nil && d.quickTaskSubmission.Input == input && d.quickTaskSubmission.DraftRevision == draftRevision
	if cached {
		title, reminder = d.quickTaskSubmission.Title, d.quickTaskSubmission.Reminder
	}
	if kind == quickInputReminder && !cached && prepared == nil {
		zone := reminderZoneName()
		if zone == "" {
			d.status = reminderMissingCopy("timezone")
			d.app.RequestUpdate()
			return
		}
		draft, err := reminders.Parse(text, time.Now(), zone)
		if err != nil {
			d.status = "The reminder could not be understood. Your text is still here; you can enter its title and time in Tasks."
			d.app.RequestUpdate()
			return
		}
		if draft.Missing != "" {
			epoch, _ := d.client.BeginMemoryRead()
			d.quickEntry = EntryState{Epoch: epoch, Pending: &draft}
			d.status = reminderMissingCopy(draft.Missing)
			d.resizeQuickPanel(false)
			d.app.RequestUpdate()
			return
		}
		title, reminder = draft.Title, draft.Reminder
	}
	if !tasks.ValidTitle(title) {
		d.status = "Enter a task title of up to 512 bytes. Your text is still here."
		d.app.RequestUpdate()
		return
	}
	if !cached {
		id, err := newTaskRequestID()
		if err != nil {
			d.status = "The task could not be prepared. Your text is still here."
			d.app.RequestUpdate()
			return
		}
		epoch, stable := d.client.BeginMemoryRead()
		if !stable {
			return
		}
		d.quickTaskSubmission = &taskSubmission{ID: id, Input: input, Title: title, Reminder: reminder, Epoch: epoch, DraftRevision: draftRevision}
	}
	requestID := d.quickTaskSubmission.ID
	epoch := d.quickTaskSubmission.Epoch
	if !d.client.MemoryReadCurrent(epoch) {
		d.status = "Memory is being changed. Your text is still here."
		d.app.RequestUpdate()
		return
	}
	request := d.quickEntryRequest
	d.loading = true
	d.status = "Saving task…"
	if reminder != nil {
		d.status = "Saving and scheduling reminder…"
	}
	d.app.RequestUpdate()
	go func() {
		result, err := d.client.createTaskAtEpoch(context.Background(), title, reminder, requestID, epoch)
		d.app.Post(func() {
			if request != d.quickEntryRequest {
				return
			}
			if !d.client.PublishMemoryRead(epoch, func() {
				d.loading = false
				if err != nil {
					d.status = "The task could not be saved. Your text is still here."
					if d.quickUI.draftRevision != draftRevision {
						d.status = "The earlier task could not be saved."
					}
					return
				}
				if d.quickUI.draftRevision == draftRevision && d.quickInput == input {
					d.quickInput = ""
				}
				d.quickTaskSubmission = nil
				receipt := taskResultCopy(result)
				d.status = receipt
				d.quickEntry = EntryState{Epoch: epoch, Targets: []assistant.TargetRef{{Kind: "task", ID: result.Item.ID, Revision: result.Item.Revision, Label: "T1"}}}
				d.quickConversation = appendQuickAction(d.quickConversation, input, receipt+" "+result.Item.Title)
				if d.quickUI.draftRevision != draftRevision {
					d.status = ""
				}
				d.showQuickCurrentAnswer()
				d.tasksLoaded = false
			}) {
				return
			}
			d.resizeQuickPanel(false)
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) tasksView() ui.View {
	if d.client == nil || d.client.reminders == nil {
		return wrapLabel("Encrypted tasks are unavailable on this build.")
	}
	filters := []ui.View{}
	for _, filter := range []struct {
		label string
		state tasks.State
	}{{"Open", tasks.Open}, {"Completed", tasks.Completed}, {"Cancelled", tasks.Cancelled}, {"All", ""}} {
		value := filter.state
		filters = append(filters, choiceButton(filter.label, d.taskFilter == value).Enabled(!d.tasksLoading).OnClick(func() { d.taskFilter = value; d.taskBefore = nil; d.loadTaskPage("") }))
	}
	if !d.taskEditing {
		filters = append(filters, ui.VBox().MainWeight(1), primaryButton("New task").Enabled(!d.loading).OnClick(func() {
			d.taskEditing = true
			d.taskEditor = nil
			d.taskTitle = ""
			d.taskWhen = ""
			d.taskTimezone = reminderZoneName()
			d.app.RequestUpdate()
		}))
	}
	rows := []ui.View{ui.HBox(filters...).Spacing(8).CrossAlign(layout.CrossCenter)}
	if d.status != "" {
		rows = append(rows, wrapLabel(d.status).Style(modern.MutedText))
	}
	if d.taskEditing {
		rows = append(rows, d.taskEditorView())
	}
	if d.tasksLoading {
		rows = append(rows, wrapLabel("Loading encrypted tasks…").Style(modern.MutedText))
	}
	for _, item := range d.taskPage.Items {
		rows = append(rows, d.taskCard(item))
	}
	if d.tasksLoaded && !d.tasksLoading && len(d.taskPage.Items) == 0 {
		message := "No tasks in this view."
		if d.taskPage.HasMore {
			message = "More saved tasks are available on the next page."
		}
		rows = append(rows, wrapLabel(message).Style(modern.MutedText))
	}
	pages := []ui.View{}
	if len(d.taskBefore) > 0 {
		pages = append(pages, flatButton("Previous page").Enabled(!d.tasksLoading).OnClick(func() {
			last := len(d.taskBefore) - 1
			after := d.taskBefore[last]
			d.taskBefore = d.taskBefore[:last]
			d.loadTaskPage(after)
		}))
	}
	if d.taskPage.HasMore {
		pages = append(pages, flatButton("Next page").Enabled(!d.tasksLoading).OnClick(func() { d.taskBefore = append(d.taskBefore, d.taskAfter); d.loadTaskPage(d.taskPage.Next) }))
	}
	rows = append(rows, ui.HBox(pages...).Spacing(8))
	return pageScroll(ui.VBox(hugButtons(rows)...).Spacing(14).CrossAlign(layout.CrossStretch))
}

func (d *desktop) taskEditorView() ui.View {
	heading := "New task or reminder"
	if d.taskEditor != nil {
		heading = "Edit task or reminder"
	}
	rows := []ui.View{wrapLabel(heading).Style(styleSectionTitle), wrapLabel("Title").Style(styleMeta), ui.TextInput().Text(d.taskTitle).OnText(func(text string) { d.taskTitle = text; d.app.RequestUpdate() }), wrapLabel("Reminder (optional)").Style(styleMeta), ui.TextInput().Text(d.taskWhen).Placeholder("tomorrow at 09:00, in 10 minutes, every friday at 16:00").OnText(func(text string) { d.taskWhen = text; d.app.RequestUpdate() }), wrapLabel("Timezone").Style(styleMeta), ui.TextInput().Text(d.taskTimezone).Placeholder("Europe/Brussels").OnText(func(text string) { d.taskTimezone = text; d.app.RequestUpdate() }), wrapLabel("Leave Reminder empty for a task without a notification. Repeating schedules use the local calendar; nonexistent times are skipped and a repeated clock uses its first occurrence.").Style(modern.MutedText)}
	if strings.TrimSpace(d.taskWhen) != "" {
		switch d.client.TaskStatus().Authorization {
		case reminders.NotDetermined:
			rows = append(rows, wrapLabel("Notifications may show your reminder text on the Lock Screen.").Style(modern.MutedText), flatButton("Allow notifications").Enabled(!d.loading).OnClick(d.requestReminderPermission))
		case reminders.Denied:
			rows = append(rows, wrapLabel("Notifications are denied in macOS System Settings. Your task will still be saved.").Style(modern.MutedText))
		}
	}
	rows = append(rows, ui.HBox(primaryButton("Save").Enabled(!d.loading && tasks.ValidTitle(strings.TrimSpace(d.taskTitle))).OnClick(d.saveTaskEditor), flatButton("Cancel edit").Enabled(!d.loading).OnClick(func() {
		d.taskEditing = false
		d.taskEditor = nil
		d.taskTitle = ""
		d.taskWhen = ""
		d.app.RequestUpdate()
	})).Spacing(8))
	return surfaceBox(14, 8, hugButtons(rows)...)
}

func (d *desktop) taskCard(item tasks.Item) ui.View {
	rows := []ui.View{wrapLabel(item.Title).Style(styleSectionTitle), wrapLabel(taskDeliveryCopy(item)).Style(styleMeta)}
	if item.Reminder != nil {
		reminder := *item.Reminder
		if item.Delivery.State == tasks.Scheduled && reminder.Repeat == tasks.Once && !item.Delivery.NextAt.IsZero() {
			reminder.DueAt = item.Delivery.NextAt
		}
		rows = append(rows, wrapLabel(reminderScheduleCopy(reminder)).Style(modern.MutedText))
		if reminder.Repeat != tasks.Once && item.Delivery.State == tasks.Scheduled && !item.Delivery.NextAt.IsZero() {
			next := reminder
			next.Repeat = tasks.Once
			next.DueAt = item.Delivery.NextAt
			rows = append(rows, wrapLabel("Next: "+reminderScheduleCopy(next)).Style(styleMeta))
		}
	}
	buttons := []ui.View{flatButton("Edit").Enabled(!d.loading).OnClick(func() { d.openTaskEditor(item) })}
	if len(item.Sources()) > 0 {
		buttons = append(buttons, flatButton("View sources").Enabled(!d.loading).OnClick(func() { d.inspectTaskSources(item) }))
	}
	if item.State == tasks.Open {
		buttons = append(buttons, flatButton("Complete").Enabled(!d.loading).OnClick(func() { d.changeTaskState(item, tasks.Completed) }), flatButton("Cancel task").Enabled(!d.loading).OnClick(func() { d.changeTaskState(item, tasks.Cancelled) }))
	} else if item.Reminder == nil {
		buttons = append(buttons, flatButton("Reopen").Enabled(!d.loading).OnClick(func() { d.changeTaskState(item, tasks.Open) }))
	} else {
		buttons = append(buttons, flatButton("Reopen with new time").Enabled(!d.loading).OnClick(func() {
			copy := tasks.Clone(item)
			copy.State = tasks.Open
			d.openTaskEditor(copy)
			d.status = "Choose a future reminder time to reopen this task."
			d.app.RequestUpdate()
		}))
	}
	if d.pendingDeleteTask == item.ID {
		buttons = append(buttons, dangerButton("Remove permanently").Enabled(!d.loading).OnClick(func() { d.removeTask(item) }), flatButton("Keep task").OnClick(func() { d.pendingDeleteTask = ""; d.app.RequestUpdate() }))
	} else {
		buttons = append(buttons, flatButton("Remove").Enabled(!d.loading).OnClick(func() { d.pendingDeleteTask = item.ID; d.app.RequestUpdate() }))
	}
	rows = append(rows, ui.HBox(buttons...).Spacing(8))
	return surfaceBox(14, 7, hugButtons(rows)...)
}

func (d *desktop) inspectTaskSources(item tasks.Item) {
	if d.client == nil || d.loading || d.askLoading {
		return
	}
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	d.invalidateAsk()
	request := d.askRequestID
	ctx, cancel := context.WithCancel(context.Background())
	d.askCancel = cancel
	d.askLoading, d.askReadingSources = true, true
	d.page, d.status, d.askQuestion = "Ask", "", "Sources for: "+item.Title
	d.app.RequestUpdate()
	go func() {
		sources, err := d.client.TaskSourceEvidence(ctx, item.ID, item.Revision)
		cancel()
		d.app.Post(func() {
			if request != d.askRequestID {
				return
			}
			if !d.client.PublishMemoryRead(epoch, func() {
				d.askLoading, d.askReadingSources, d.askCancel = false, false, nil
				if err != nil {
					if d.page == "Ask" {
						d.status = "Task sources could not be read. Refresh the task and try again."
					}
					return
				}
				ids := make([]string, 0, len(sources))
				for _, source := range sources {
					ids = append(ids, source.SourceID)
				}
				d.askResult = &assistant.Result{Direct: true, Answer: assistant.Answer{Text: "This task was created from these saved sources. Its current title and reminder are managed in Tasks.", SourceIDs: ids}, Sources: sources}
			}) {
				return
			}
			d.app.RequestUpdate()
		})
	}()
}

func taskWhenText(item tasks.Item) string {
	if item.Reminder == nil {
		return ""
	}
	r := item.Reminder
	if r.Repeat == tasks.Daily {
		return fmt.Sprintf("every day at %02d:%02d", r.Hour, r.Minute)
	}
	if r.Repeat == tasks.Weekly {
		return fmt.Sprintf("every %s at %02d:%02d", strings.ToLower(r.Weekday.String()), r.Hour, r.Minute)
	}
	zone, _ := time.LoadLocation(r.Timezone)
	return "on " + r.DueAt.In(zone).Format("2006-01-02 at 15:04")
}
func (d *desktop) openTaskEditor(item tasks.Item) {
	copy := tasks.Clone(item)
	d.page = "Tasks"
	d.taskEditing = true
	d.taskEditor = &copy
	d.taskTitle = item.Title
	d.taskWhen = taskWhenText(item)
	d.taskOriginalWhen = d.taskWhen
	d.taskTimezone = reminderZoneName()
	if item.Reminder != nil {
		d.taskTimezone = item.Reminder.Timezone
	}
	d.app.RequestUpdate()
}

func (d *desktop) saveTaskEditor() {
	if d.loading || d.client == nil {
		return
	}
	title := strings.TrimSpace(d.taskTitle)
	var reminder *tasks.Reminder
	if strings.TrimSpace(d.taskWhen) != "" {
		if d.taskEditor != nil && d.taskEditor.Reminder != nil && d.taskWhen == d.taskOriginalWhen && d.taskTimezone == d.taskEditor.Reminder.Timezone {
			r := *d.taskEditor.Reminder
			reminder = &r
		} else {
			draft, err := reminders.Parse("reminder: "+strings.TrimSpace(d.taskWhen)+" to "+title, time.Now(), strings.TrimSpace(d.taskTimezone))
			if err != nil {
				d.status = "Enter a valid IANA timezone and reminder time."
				d.app.RequestUpdate()
				return
			}
			if draft.Missing != "" {
				d.status = reminderMissingCopy(draft.Missing)
				d.app.RequestUpdate()
				return
			}
			reminder = draft.Reminder
		}
	}
	var edit *tasks.Item
	if d.taskEditor != nil {
		copy := tasks.Clone(*d.taskEditor)
		copy.Title = title
		copy.Reminder = reminder
		edit = &copy
	}
	d.performTaskChange(func(epoch uint64) (reminders.Result, error) {
		if edit != nil {
			return d.client.updateTaskWithEpoch(context.Background(), *edit, &epoch)
		}
		return d.client.createTaskAtEpoch(context.Background(), title, reminder, "", epoch)
	}, true)
}
func (d *desktop) changeTaskState(item tasks.Item, state tasks.State) {
	item.State = state
	d.performTaskChange(func(epoch uint64) (reminders.Result, error) {
		return d.client.updateTaskWithEpoch(context.Background(), item, &epoch)
	}, false)
}
func (d *desktop) performTaskChange(action func(uint64) (reminders.Result, error), closeEditor bool) {
	if d.loading || d.client == nil {
		return
	}
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	d.loading = true
	d.app.RequestUpdate()
	go func() {
		result, err := action(epoch)
		d.app.Post(func() {
			if !d.client.PublishMemoryRead(epoch, func() {
				d.loading = false
				if err != nil {
					d.status = "The task could not be changed. Check notification delivery and refresh the task; your edit is still here."
					return
				}
				d.status = taskResultCopy(result)
				if closeEditor {
					d.taskEditing = false
					d.taskEditor = nil
					d.taskTitle = ""
					d.taskWhen = ""
				}
			}) {
				return
			}
			switch d.page {
			case "Today":
				d.loadToday()
			case "Ask", "Search":
				d.runSearch()
			default:
				d.loadTaskPage(d.taskAfter)
			}
			d.app.RequestUpdate()
		})
	}()
}
func (d *desktop) removeTask(item tasks.Item) {
	if d.loading || d.client == nil {
		return
	}
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	d.loading = true
	d.app.RequestUpdate()
	go func() {
		err := d.client.deleteTaskAtEpoch(context.Background(), item.ID, item.Revision, epoch)
		postEpoch, stable := d.client.BeginMemoryRead()
		d.app.Post(func() {
			if !stable || !d.client.PublishMemoryRead(postEpoch, func() {
				d.loading = false
				if err != nil {
					d.status = "The task could not be removed. Its notification cancellation was not confirmed."
				} else {
					d.status = "Task removed and its notifications canceled."
					d.pendingDeleteTask = ""
					if d.taskEditor != nil && d.taskEditor.ID == item.ID {
						d.taskEditor = nil
						d.taskEditing = false
					}
				}
			}) {
				return
			}
			switch d.page {
			case "Today":
				d.loadToday()
			case "Ask", "Search":
				d.runSearch()
			default:
				d.loadTaskPage(d.taskAfter)
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) loadTaskPage(after string) {
	if d.client == nil || d.tasksLoading {
		return
	}
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	d.tasksLoading = true
	d.taskRequest++
	request := d.taskRequest
	filter := d.taskFilter
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		page, err := d.client.TaskPage(ctx, after, filter)
		d.app.Post(func() {
			if request != d.taskRequest {
				return
			}
			if !d.client.PublishMemoryRead(epoch, func() {
				d.tasksLoading = false
				if err != nil {
					d.status = "Tasks could not be read from encrypted storage."
					return
				}
				d.taskPage = page
				d.taskAfter = after
				d.tasksLoaded = true
			}) {
				return
			}
			if d.taskRefreshPending {
				d.taskRefreshPending = false
				d.loadTaskPage(d.taskAfter)
			}
			d.app.RequestUpdate()
		})
	}()
}
func (d *desktop) requestReminderPermission() {
	if d.loading || d.client == nil {
		return
	}
	d.loading = true
	d.app.RequestUpdate()
	go func() {
		auth, err := d.client.RequestReminderAuthorization(context.Background())
		d.app.Post(func() {
			d.loading = false
			if err != nil {
				d.status = "Notification permission could not be checked. Your tasks remain saved."
			} else if auth == reminders.Allowed {
				d.status = "Notifications allowed. Saved reminders are being refreshed."
			} else {
				d.status = "Notifications are not allowed. You can change this in macOS System Settings."
			}
			d.app.RequestUpdate()
		})
	}()
}
func (d *desktop) openTaskFromNotice(id string) {
	if d.client == nil {
		return
	}
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		item, err := d.client.Task(ctx, id)
		d.app.Post(func() {
			if !d.client.PublishMemoryRead(epoch, func() {
				d.dashboardOpen = true
				d.page = "Tasks"
				if err != nil {
					d.status = "This reminder is no longer saved."
				} else {
					d.openTaskEditor(item)
				}
			}) {
				return
			}
			d.loadTaskPage("")
			d.app.RequestUpdate()
		})
	}()
}
