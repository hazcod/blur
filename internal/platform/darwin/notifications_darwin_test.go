//go:build darwin

package darwin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ebitengine/purego/objc"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/tasks"
)

func TestNativeNotificationRequestUsesTaskTextAndExactUTCTrigger(t *testing.T) {
	if err := loadNoticeLibraries(); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	withAutoreleasePool(func() {
		due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
		n := reminders.Notice{ID: reminders.NotificationPrefix + "1234567890abcdef1234567890abcdef.1", Title: "Get eggs on Monday", Schedule: tasks.Reminder{DueAt: due, Timezone: "Europe/Brussels"}}
		request, err := noticeRequest(n)
		if err != nil {
			t.Fatal(err)
		}
		content := objc.Send[objc.ID](request, sel("content"))
		title, _ := goStringFrom(objc.Send[objc.ID](content, sel("title")))
		body, _ := goStringFrom(objc.Send[objc.ID](content, sel("body")))
		if title != "Reminder" || body != n.Title {
			t.Fatal("notification lost intended task text", title, body)
		}
		trigger := objc.Send[objc.ID](request, sel("trigger"))
		date := objc.Send[objc.ID](trigger, sel("nextTriggerDate"))
		seconds := objc.Send[float64](date, sel("timeIntervalSince1970"))
		if int64(seconds) != due.Unix() || objc.Send[bool](trigger, sel("repeats")) {
			t.Fatal("incorrect one-time native schedule", seconds, due)
		}
		zone := objc.Send[objc.ID](objc.Send[objc.ID](trigger, sel("dateComponents")), sel("timeZone"))
		name, _ := goStringFrom(objc.Send[objc.ID](zone, sel("name")))
		if name != "GMT" && name != "UTC" {
			t.Fatal("one-time trigger changed with system timezone", name)
		}
	})
}

func TestNativeDailyTriggerRetainsIANAZoneAndCalendarClock(t *testing.T) {
	if err := loadNoticeLibraries(); err != nil {
		t.Fatal(err)
	}
	zone, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	local := now.In(zone)
	date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	due, err := tasks.ResolveWallTime(date, "09:00", "Europe/Brussels", "")
	if err != nil {
		t.Fatal(err)
	}
	r := tasks.Reminder{DueAt: due, Timezone: "Europe/Brussels", Repeat: tasks.Daily, Hour: 9}
	due, err = tasks.NextOccurrence(r, now)
	if err != nil {
		t.Fatal(err)
	}
	r.DueAt = due
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	withAutoreleasePool(func() {
		request, err := noticeRequest(reminders.Notice{ID: reminders.NotificationPrefix + "1234567890abcdef1234567890abcdef.2", Title: "Review logs", Schedule: r})
		if err != nil {
			t.Fatal(err)
		}
		trigger := objc.Send[objc.ID](request, sel("trigger"))
		components := objc.Send[objc.ID](trigger, sel("dateComponents"))
		nativeZone := objc.Send[objc.ID](components, sel("timeZone"))
		name, _ := goStringFrom(objc.Send[objc.ID](nativeZone, sel("name")))
		if name != "Europe/Brussels" || objc.Send[int64](components, sel("hour")) != 9 || !objc.Send[bool](trigger, sel("repeats")) {
			t.Fatal("daily native schedule lost calendar/timezone")
		}
	})
}

func TestNativeNotificationCompletionUsesActualBlockABI(t *testing.T) {
	if err := loadNoticeLibraries(); err != nil {
		t.Fatal(err)
	}
	var options uint64
	block := objc.NewBlock(func(_ objc.Block, value uint64) { options = value })
	defer block.Release()
	expected := uint64(26)
	invokeNoticeCompletion(block, &expected)
	if options != expected {
		t.Fatal("presentation completion did not execute", options)
	}
	called := false
	done := objc.NewBlock(func(_ objc.Block) { called = true })
	defer done.Release()
	invokeNoticeCompletion(done, nil)
	if !called {
		t.Fatal("response completion did not execute")
	}
}

var nativeNoticeBlockTest struct {
	once    sync.Once
	class   objc.Class
	mu      sync.Mutex
	done    chan bool
	invoked bool
}

// NSURLSession supplies a genuine framework-created completion block. This
// checks native-to-Go-to-native block invocation, rather than testing only a
// block produced by purego. Traffic is confined to a synthetic loopback server;
// no notification is posted and no user permission or UI state is changed.
func TestNotificationCompletionInvokesFrameworkCreatedBlock(t *testing.T) {
	if err := loadNoticeLibraries(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("synthetic native ABI fixture")) }))
	defer server.Close()
	nativeNoticeBlockTest.once.Do(func() {
		class, err := objc.RegisterClass("BlurTestFrameworkCompletionReceiver", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
			{Cmd: sel("URLSession:dataTask:didReceiveResponse:completionHandler:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID, _ objc.ID, _ objc.ID, completion objc.Block) {
				nativeNoticeBlockTest.mu.Lock()
				nativeNoticeBlockTest.invoked = true
				nativeNoticeBlockTest.mu.Unlock()
				allow := uint64(1)
				invokeNoticeCompletion(completion, &allow)
			}},
			{Cmd: sel("URLSession:task:didCompleteWithError:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID, _ objc.ID, nativeError objc.ID) {
				nativeNoticeBlockTest.mu.Lock()
				done := nativeNoticeBlockTest.done
				nativeNoticeBlockTest.mu.Unlock()
				done <- nativeError == 0
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		nativeNoticeBlockTest.class = class
	})
	nativeNoticeBlockTest.mu.Lock()
	nativeNoticeBlockTest.done = make(chan bool, 1)
	nativeNoticeBlockTest.invoked = false
	done := nativeNoticeBlockTest.done
	nativeNoticeBlockTest.mu.Unlock()
	var session, delegate objc.ID
	withNoticePool(func() {
		delegate = objc.Send[objc.ID](objc.Send[objc.ID](objc.ID(nativeNoticeBlockTest.class), sel("alloc")), sel("init"))
		config := objc.Send[objc.ID](objc.ID(objc.GetClass("NSURLSessionConfiguration")), sel("ephemeralSessionConfiguration"))
		objc.Send[objc.ID](config, sel("setTimeoutIntervalForRequest:"), float64(5))
		session = objc.Send[objc.ID](objc.ID(objc.GetClass("NSURLSession")), sel("sessionWithConfiguration:delegate:delegateQueue:"), config, delegate, objc.ID(0))
		objc.Send[objc.ID](session, sel("retain"))
		url := objc.Send[objc.ID](objc.ID(objc.GetClass("NSURL")), sel("URLWithString:"), nsStringFrom(server.URL))
		task := objc.Send[objc.ID](session, sel("dataTaskWithURL:"), url)
		objc.Send[objc.ID](task, sel("resume"))
	})
	defer withNoticePool(func() {
		objc.Send[objc.ID](session, sel("invalidateAndCancel"))
		objc.Send[objc.ID](session, sel("release"))
		objc.Send[objc.ID](delegate, sel("release"))
	})
	select {
	case succeeded := <-done:
		if !succeeded {
			t.Fatal("framework callback failed before native completion finished")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("framework completion block was not invoked")
	}
	nativeNoticeBlockTest.mu.Lock()
	invoked := nativeNoticeBlockTest.invoked
	nativeNoticeBlockTest.mu.Unlock()
	if !invoked {
		t.Fatal("did not receive a framework-created block")
	}
}

type testNoticeCenter struct {
	mu             sync.Mutex
	pending        map[string]objc.ID
	delayedRequest objc.ID
	delayedBlock   objc.Block
	delay          bool
	entered        chan struct{}
	removes        int
}

var testNoticeClasses struct {
	once     sync.Once
	class    objc.Class
	settings objc.ID
	owners   map[objc.ID]*testNoticeCenter
	mu       sync.Mutex
}

func newTestNoticeCenter(t *testing.T) (*Notifications, *testNoticeCenter) {
	t.Helper()
	if err := loadNoticeLibraries(); err != nil {
		t.Fatal(err)
	}
	testNoticeClasses.once.Do(func() {
		settingsClass, err := objc.RegisterClass("BlurTestNoticeSettings", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{{Cmd: sel("authorizationStatus"), Fn: func(_ objc.ID, _ objc.SEL) int64 { return 2 }}})
		if err != nil {
			t.Fatal(err)
		}
		testNoticeClasses.settings = objc.Send[objc.ID](objc.Send[objc.ID](objc.ID(settingsClass), sel("alloc")), sel("init"))
		testNoticeClasses.owners = map[objc.ID]*testNoticeCenter{}
		owner := func(id objc.ID) *testNoticeCenter {
			testNoticeClasses.mu.Lock()
			defer testNoticeClasses.mu.Unlock()
			return testNoticeClasses.owners[id]
		}
		class, err := objc.RegisterClass("BlurTestNoticeCenter", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
			{Cmd: sel("getNotificationSettingsWithCompletionHandler:"), Fn: func(_ objc.ID, _ objc.SEL, block objc.Block) { block.Invoke(testNoticeClasses.settings) }},
			{Cmd: sel("addNotificationRequest:withCompletionHandler:"), Fn: func(self objc.ID, _ objc.SEL, request objc.ID, block objc.Block) {
				o := owner(self)
				o.mu.Lock()
				if o.delay {
					o.delayedRequest = objc.Send[objc.ID](request, sel("retain"))
					o.delayedBlock = block.Copy()
					o.mu.Unlock()
					o.entered <- struct{}{}
					return
				}
				id, _ := goStringFrom(objc.Send[objc.ID](request, sel("identifier")))
				o.pending[id] = objc.Send[objc.ID](request, sel("retain"))
				o.mu.Unlock()
				block.Invoke(objc.ID(0))
			}},
			{Cmd: sel("getPendingNotificationRequestsWithCompletionHandler:"), Fn: func(self objc.ID, _ objc.SEL, block objc.Block) {
				withAutoreleasePool(func() {
					o := owner(self)
					o.mu.Lock()
					array := objc.Send[objc.ID](objc.ID(objc.GetClass("NSMutableArray")), sel("array"))
					for _, request := range o.pending {
						objc.Send[objc.ID](array, sel("addObject:"), request)
					}
					o.mu.Unlock()
					block.Invoke(array)
				})
			}},
			{Cmd: sel("getDeliveredNotificationsWithCompletionHandler:"), Fn: func(_ objc.ID, _ objc.SEL, block objc.Block) {
				withAutoreleasePool(func() { block.Invoke(objc.Send[objc.ID](objc.ID(objc.GetClass("NSArray")), sel("array"))) })
			}},
			{Cmd: sel("removePendingNotificationRequestsWithIdentifiers:"), Fn: func(self objc.ID, _ objc.SEL, array objc.ID) {
				o := owner(self)
				o.mu.Lock()
				defer o.mu.Unlock()
				o.removes++
				count := objc.Send[uint64](array, sel("count"))
				for i := uint64(0); i < count; i++ {
					id, _ := goStringFrom(objc.Send[objc.ID](array, sel("objectAtIndex:"), i))
					if request := o.pending[id]; request != 0 {
						objc.Send[objc.ID](request, sel("release"))
						delete(o.pending, id)
					}
				}
			}},
			{Cmd: sel("removeDeliveredNotificationsWithIdentifiers:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {}},
		})
		if err != nil {
			t.Fatal(err)
		}
		testNoticeClasses.class = class
	})
	center := objc.Send[objc.ID](objc.Send[objc.ID](objc.ID(testNoticeClasses.class), sel("alloc")), sel("init"))
	state := &testNoticeCenter{pending: map[string]objc.ID{}, entered: make(chan struct{}, 1)}
	testNoticeClasses.mu.Lock()
	testNoticeClasses.owners[center] = state
	testNoticeClasses.mu.Unlock()
	p := NewNotifications()
	p.once.Do(func() { p.center = center })
	t.Cleanup(func() {
		testNoticeClasses.mu.Lock()
		delete(testNoticeClasses.owners, center)
		testNoticeClasses.mu.Unlock()
		for _, request := range state.pending {
			objc.Send[objc.ID](request, sel("release"))
		}
		objc.Send[objc.ID](center, sel("release"))
	})
	return p, state
}

func TestNativeUncertainAddMustFinishBeforeCancellationCanSucceed(t *testing.T) {
	p, state := newTestNoticeCenter(t)
	state.delay = true
	n := reminders.Notice{ID: reminders.NotificationPrefix + "1234567890abcdef1234567890abcdef.3", Title: "Review logs", Schedule: tasks.Reminder{DueAt: time.Now().Add(time.Hour), Timezone: "UTC"}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Queue(ctx, n) }()
	select {
	case <-state.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("native add did not enter")
	}
	cancel()
	if err := <-done; !errors.Is(err, reminders.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	short, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if err := p.Remove(short, []string{n.ID}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("cancel passed unresolved native add", err)
	}
	state.mu.Lock()
	if state.removes != 0 {
		t.Fatal("cancel overtook in-flight add")
	}
	request, block := state.delayedRequest, state.delayedBlock
	state.pending[n.ID] = request
	state.delayedRequest = 0
	state.delayedBlock = 0
	state.mu.Unlock()
	block.Invoke(objc.ID(0))
	block.Release()
	if err := p.Remove(context.Background(), []string{n.ID}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := p.Snapshot(context.Background())
	if err != nil || len(snapshot.Pending) != 0 {
		t.Fatal(snapshot, err)
	}
}

func TestNativePendingSnapshotIncludesActualNextTriggerDate(t *testing.T) {
	p, _ := newTestNoticeCenter(t)
	due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	n := reminders.Notice{ID: reminders.NotificationPrefix + "1234567890abcdef1234567890abcdef.4", Title: "Review logs", Schedule: tasks.Reminder{DueAt: due, Timezone: "UTC"}}
	if err := p.Queue(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	snapshot, err := p.Snapshot(context.Background())
	if err != nil || len(snapshot.Pending) != 1 || !snapshot.NextAt[n.ID].Equal(due) || snapshot.ContentVersions[n.ID] != reminders.NotificationContentVersion {
		t.Fatalf("native pending time not preserved: %+v %v", snapshot, err)
	}
	if err := p.Remove(context.Background(), []string{n.ID}); err != nil {
		t.Fatal(err)
	}
}

func TestNativePendingSnapshotMarksLegacyContentVersionExplicitly(t *testing.T) {
	if err := loadNoticeLibraries(); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	withAutoreleasePool(func() {
		id := reminders.NotificationPrefix + "1234567890abcdef1234567890abcdef.6"
		// Build a legacy payload directly. No content title/body is read by the
		// migration probe; its missing metadata alone identifies version zero.
		content := objc.Send[objc.ID](objc.Send[objc.ID](objc.ID(objc.GetClass("UNMutableNotificationContent")), sel("alloc")), sel("init"))
		defer objc.Send[objc.ID](content, sel("release"))
		request := objc.Send[objc.ID](objc.ID(objc.GetClass("UNNotificationRequest")), sel("requestWithIdentifier:content:trigger:"), nsStringFrom(id), content, objc.ID(0))
		array := objc.Send[objc.ID](objc.ID(objc.GetClass("NSArray")), sel("arrayWithObject:"), request)
		got := readNoticeIDs(array, false)
		version, known := got.versions[id]
		if got.err != nil || len(got.ids) != 1 || !known || version != 0 {
			t.Fatal("legacy pending payload was indistinguishable from an unknown port", got.err)
		}
	})
}
