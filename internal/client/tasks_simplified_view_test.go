package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/layout"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/tasks"
)

func simplifiedTaskTree(t *testing.T, d *desktop) (map[string][]*gui.Button, string) {
	t.Helper()
	buttons := map[string][]*gui.Button{}
	var text strings.Builder
	var walk func(gui.Widget)
	walk = func(widget gui.Widget) {
		if label, ok := widget.(*gui.Label); ok {
			text.WriteString(label.Text() + "\n")
		}
		if button, ok := widget.(*gui.Button); ok {
			if label, ok := button.Child().(*gui.Label); ok {
				buttons[label.Text()] = append(buttons[label.Text()], button)
			}
		}
		for _, child := range widget.Children() {
			walk(child)
		}
	}
	walk(mountVoicePopupView(t, d.tasksView()))
	return buttons, text.String()
}

func TestTasksMountedHeaderKeepsNewTaskOnFilterRowWithoutDeliveryPane(t *testing.T) {
	c, _, at := todayTaskClient(t)
	d := &desktop{client: c, app: &libraryRefreshApp{}, tasksLoaded: true, taskFilter: tasks.Open, status: "Reminder scheduling needs attention."}
	d.taskPage.Items = []tasks.Item{{Title: "Review Aurora", State: tasks.Open, Reminder: &tasks.Reminder{DueAt: at.Add(time.Hour), Timezone: "UTC", Repeat: tasks.Once}, Delivery: tasks.Delivery{State: tasks.Failed}}}
	buttons, text := simplifiedTaskTree(t, d)
	if strings.Contains(text, "Reminder delivery") || strings.Contains(text, "Refresh reminder delivery") || !strings.Contains(text, d.status) {
		t.Fatal("Tasks retained the delivery pane or hid inline status", text)
	}
	if !strings.Contains(text, "Saved; notification scheduling failed") {
		t.Fatal("removing the pane also hid the task's own scheduling error", text)
	}
	if len(buttons["New task"]) != 1 {
		t.Fatal("Tasks lacks its single creation action", text)
	}
	row := buttons["New task"][0].Parent()
	linear, ok := row.LayoutManager().(*layout.LinearLayout)
	if !ok || linear.Direction != layout.DirectionHorizontal {
		t.Fatal("New task is not in a horizontal control row")
	}
	for _, filter := range []string{"Open", "Completed", "Cancelled", "All"} {
		if len(buttons[filter]) != 1 || buttons[filter][0].Parent() != row {
			t.Errorf("%q is not on the same mounted row as New task", filter)
		}
	}
	clickMemoryButton(t, buttons, "New task")
	if !d.taskEditing || d.taskEditor != nil || d.taskTitle != "" {
		t.Fatal("mounted creation action did not open the blank task editor")
	}
}

type taskEditorPermissionPort struct {
	clientNoticePort
	requested int
}

func (*taskEditorPermissionPort) Authorization(context.Context) (reminders.Authorization, error) {
	return reminders.NotDetermined, nil
}
func (p *taskEditorPermissionPort) RequestAuthorization(context.Context) (reminders.Authorization, error) {
	p.requested++
	return reminders.Allowed, nil
}

func TestTasksMountedReminderEditorRetainsExplicitPermissionActionInline(t *testing.T) {
	c, store, _ := todayTaskClient(t)
	port := &taskEditorPermissionPort{}
	service, err := reminders.NewService(store, port, reminders.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	if err := service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.reminders = service
	app := &libraryRefreshApp{posted: make(chan func(), 2)}
	d := &desktop{client: c, app: app, taskEditing: true, taskTitle: "Review Aurora", taskWhen: "tomorrow at 9am"}
	buttons, text := simplifiedTaskTree(t, d)
	if strings.Contains(text, "Reminder delivery") || strings.Contains(text, "Refresh reminder delivery") || len(buttons["Allow notifications"]) != 1 {
		t.Fatal("reminder editor lost its permission action or kept the removed pane", text)
	}
	if port.requested != 0 {
		t.Fatal("rendering requested notification permission")
	}
	clickMemoryButton(t, buttons, "Allow notifications")
	publishLibraryCallback(t, app)
	if port.requested != 1 || !strings.Contains(d.status, "Notifications allowed") {
		t.Fatal("explicit inline permission action did not complete", port.requested, d.status)
	}
}
