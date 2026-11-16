package darwin

import (
	"runtime"

	"github.com/ebitengine/purego/objc"
	"github.com/hazcod/blur/internal/reminders"
)

// ReminderContentForTest constructs only in-memory native content. It does not
// access a notification center, request permission or deliver a notification.
// This test-only export keeps durable-store integration outside this package's
// capture → darwin production dependency cycle.
func ReminderContentForTest(notice reminders.Notice) (title, body string, version uint8, err error) {
	if err = loadNoticeLibraries(); err != nil {
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	withAutoreleasePool(func() {
		var request objc.ID
		request, err = noticeRequest(notice)
		if err != nil {
			return
		}
		content := objc.Send[objc.ID](request, sel("content"))
		title, _ = goStringFrom(objc.Send[objc.ID](content, sel("title")))
		body, _ = goStringFrom(objc.Send[objc.ID](content, sel("body")))
		version = nativeNoticeContentVersion(request)
	})
	return
}
