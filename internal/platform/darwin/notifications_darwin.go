//go:build darwin

package darwin

import (
	"context"
	"math"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	nativeffi "github.com/goexlib/cgo"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/tasks"
)

// Notifications uses UserNotifications, not an in-process timer. Queued notices
// survive process exit. SDK references: UNUserNotificationCenter.h,
// UNNotificationTrigger.h; https://developer.apple.com/documentation/usernotifications/scheduling-a-notification-locally-from-your-app
type Notifications struct {
	once   sync.Once
	center objc.ID
	err    error
	gate   chan struct{}
	opens  chan string
}

func NewNotifications() *Notifications {
	return &Notifications{gate: make(chan struct{}, 1), opens: make(chan string, 8)}
}

// Prepare installs the delegate during bootstrap, before the AppKit launch
// finishes. It never requests notification permission or schedules a notice.
func (p *Notifications) Prepare() error        { return p.ensure() }
func (p *Notifications) Opened() <-chan string { return p.opens }

var noticeLibraries struct {
	once            sync.Once
	err             error
	copyBlockHeader func(unsafe.Pointer, objc.Block, uintptr) uintptr
	defaultAction   objc.ID
}

func loadNoticeLibraries() error {
	noticeLibraries.once.Do(func() {
		var notificationLibrary uintptr
		for _, path := range []string{"/System/Library/Frameworks/Foundation.framework/Foundation", "/System/Library/Frameworks/UserNotifications.framework/UserNotifications"} {
			lib, err := purego.Dlopen(path, purego.RTLD_GLOBAL|purego.RTLD_NOW)
			if err != nil {
				noticeLibraries.err = reminders.ErrUnavailable
				return
			}
			notificationLibrary = lib
		}
		action, err := nativeffi.GetExternVariant[uintptr](notificationLibrary, "UNNotificationDefaultActionIdentifier")
		if err != nil || action == nil || *action == 0 {
			noticeLibraries.err = reminders.ErrUnavailable
			return
		}
		noticeLibraries.defaultAction = objc.ID(*action)
		lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_GLOBAL|purego.RTLD_NOW)
		if err != nil {
			noticeLibraries.err = reminders.ErrUnavailable
			return
		}
		purego.RegisterLibFunc(&noticeLibraries.copyBlockHeader, lib, "memcpy")
	})
	return noticeLibraries.err
}

// A native completion block is not one of purego's Go-owned blocks. Invoke its
// ABI function pointer directly, using memcpy to read native memory without a
// Go uintptr-to-pointer conversion. Apple/Clang ABI: https://clang.llvm.org/docs/Block-ABI-Apple.html
func invokeNoticeCompletion(block objc.Block, options *uint64) {
	if block == 0 || noticeLibraries.copyBlockHeader == nil {
		return
	}
	var header struct {
		isa             uintptr
		flags, reserved uint32
		invoke          uintptr
	}
	noticeLibraries.copyBlockHeader(unsafe.Pointer(&header), block, unsafe.Sizeof(header)) // #nosec G103 -- Copy exactly the fixed Apple Block ABI header into the live local struct.
	if header.invoke == 0 {
		return
	}
	if options == nil {
		var call func(objc.Block)
		purego.RegisterFunc(&call, header.invoke)
		call(block)
	} else {
		var call func(objc.Block, uint64)
		purego.RegisterFunc(&call, header.invoke)
		call(block, *options)
	}
}

var noticeDelegate struct {
	mu     sync.Mutex
	class  objc.Class
	owners map[objc.ID]*Notifications
}

func newNoticeDelegate(p *Notifications) (objc.ID, error) {
	noticeDelegate.mu.Lock()
	defer noticeDelegate.mu.Unlock()
	if noticeDelegate.class == 0 {
		class, err := objc.RegisterClass("BlurReminderNotificationDelegate", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
			{Cmd: sel("userNotificationCenter:willPresentNotification:withCompletionHandler:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID, _ objc.ID, completion objc.Block) {
				options := uint64((1 << 1) | (1 << 3) | (1 << 4))
				invokeNoticeCompletion(completion, &options)
			}},
			{Cmd: sel("userNotificationCenter:didReceiveNotificationResponse:withCompletionHandler:"), Fn: func(self objc.ID, _ objc.SEL, _ objc.ID, response objc.ID, completion objc.Block) {
				defer invokeNoticeCompletion(completion, nil)
				action := objc.Send[objc.ID](response, sel("actionIdentifier"))
				if action == 0 || !objc.Send[bool](action, sel("isEqualToString:"), noticeLibraries.defaultAction) {
					return
				}
				notification := objc.Send[objc.ID](response, sel("notification"))
				request := objc.Send[objc.ID](notification, sel("request"))
				id, ok := goStringFrom(objc.Send[objc.ID](request, sel("identifier")))
				if !ok {
					return
				}
				taskID, _, valid := reminders.NotificationTaskID(id)
				if !valid {
					return
				}
				noticeDelegate.mu.Lock()
				owner := noticeDelegate.owners[self]
				noticeDelegate.mu.Unlock()
				if owner != nil {
					select {
					case owner.opens <- taskID:
					default:
					}
				}
			}},
		})
		if err != nil {
			return 0, reminders.ErrUnavailable
		}
		noticeDelegate.class = class
		noticeDelegate.owners = map[objc.ID]*Notifications{}
	}
	delegate := objc.Send[objc.ID](objc.Send[objc.ID](objc.ID(noticeDelegate.class), sel("alloc")), sel("init"))
	if delegate == 0 {
		return 0, reminders.ErrUnavailable
	}
	// The center holds a weak delegate. Keep this one process-lifetime object
	// retained so foreground presentation and cold-launch responses can finish.
	noticeDelegate.owners[delegate] = p
	return delegate, nil
}

func (p *Notifications) ensure() error {
	p.once.Do(func() {
		if err := loadNoticeLibraries(); err != nil {
			p.err = err
			return
		}
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		withAutoreleasePool(func() {
			bundle := objc.Send[objc.ID](objc.ID(objc.GetClass("NSBundle")), sel("mainBundle"))
			id, ok := goStringFrom(objc.Send[objc.ID](bundle, sel("bundleIdentifier")))
			path, pathOK := goStringFrom(objc.Send[objc.ID](bundle, sel("bundlePath")))
			// Calling currentNotificationCenter from an unbundled Go test/CLI
			// can raise an Objective-C exception. Never make that call there.
			if !ok || id != appidentity.BundleID || !pathOK || !strings.HasSuffix(path, ".app") {
				p.err = reminders.ErrUnsupported
				return
			}
			center := objc.Send[objc.ID](objc.ID(objc.GetClass("UNUserNotificationCenter")), sel("currentNotificationCenter"))
			if center == 0 {
				p.err = reminders.ErrUnavailable
				return
			}
			delegate, err := newNoticeDelegate(p)
			if err != nil {
				p.err = err
				return
			}
			objc.Send[objc.ID](center, sel("setDelegate:"), delegate)
			p.center = objc.Send[objc.ID](center, sel("retain"))
		})
	})
	return p.err
}

func (p *Notifications) acquire(ctx context.Context) (func(), error) {
	if ctx == nil {
		return nil, tasks.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := p.ensure(); err != nil {
		return nil, err
	}
	select {
	case p.gate <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-p.gate }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func noticeAuthorization(raw int64) reminders.Authorization {
	switch raw {
	case 0:
		return reminders.NotDetermined
	case 1:
		return reminders.Denied
	case 2, 3:
		return reminders.Allowed
	default:
		return reminders.Denied
	}
}

func withNoticePool(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	withAutoreleasePool(fn)
}
func (p *Notifications) Authorization(ctx context.Context) (reminders.Authorization, error) {
	if ctx == nil {
		return "", tasks.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := p.ensure(); err != nil {
		if err == reminders.ErrUnsupported {
			return reminders.Unsupported, nil
		}
		return "", err
	}
	result := make(chan reminders.Authorization, 1)
	block := objc.NewBlock(func(_ objc.Block, settings objc.ID) {
		withNoticePool(func() {
			raw := objc.Send[int64](settings, sel("authorizationStatus"))
			result <- noticeAuthorization(raw)
		})
	})
	defer block.Release()
	withNoticePool(func() { objc.Send[objc.ID](p.center, sel("getNotificationSettingsWithCompletionHandler:"), block) })
	select {
	case auth := <-result:
		return auth, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (p *Notifications) RequestAuthorization(ctx context.Context) (reminders.Authorization, error) {
	if ctx == nil {
		return "", tasks.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := p.ensure(); err != nil {
		if err == reminders.ErrUnsupported {
			return reminders.Unsupported, nil
		}
		return "", err
	}
	type answer struct {
		granted bool
		failed  bool
	}
	result := make(chan answer, 1)
	block := objc.NewBlock(func(_ objc.Block, granted bool, nativeError objc.ID) {
		result <- answer{granted: granted, failed: nativeError != 0}
	})
	defer block.Release()
	withNoticePool(func() {
		objc.Send[objc.ID](p.center, sel("requestAuthorizationWithOptions:completionHandler:"), uint64((1<<1)|(1<<2)), block)
	})
	select {
	case got := <-result:
		if got.failed {
			return "", reminders.ErrUnavailable
		}
		if got.granted {
			return reminders.Allowed, nil
		}
		return reminders.Denied, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type noticeIDs struct {
	ids      []string
	err      error
	next     map[string]time.Time
	versions map[string]uint8
}

const noticeContentVersionKey = "blur.reminder.content-version"
const noticeContentVersionValue = "task-title-v1"

func nativeNoticeContentVersion(request objc.ID) uint8 {
	content := objc.Send[objc.ID](request, sel("content"))
	if content == 0 {
		return 0
	}
	info := objc.Send[objc.ID](content, sel("userInfo"))
	if info == 0 {
		return 0
	}
	value := objc.Send[objc.ID](info, sel("objectForKey:"), nsStringFrom(noticeContentVersionKey))
	if value == 0 || !objc.Send[bool](value, sel("isKindOfClass:"), objc.ID(objc.GetClass("NSString"))) {
		return 0
	}
	version, ok := goStringFrom(value)
	if !ok || version != noticeContentVersionValue {
		return 0
	}
	return reminders.NotificationContentVersion
}

func readNoticeIDs(array objc.ID, delivered bool) noticeIDs {
	if array == 0 {
		return noticeIDs{err: reminders.ErrUnavailable}
	}
	count := objc.Send[uint64](array, sel("count"))
	if count > reminders.MaxSnapshotIDs {
		return noticeIDs{err: tasks.ErrCapacity}
	}
	ids := make([]string, 0, int(count))
	nextTimes := map[string]time.Time{}
	versions := map[string]uint8{}
	for i := uint64(0); i < count; i++ {
		item := objc.Send[objc.ID](array, sel("objectAtIndex:"), i)
		if delivered {
			item = objc.Send[objc.ID](item, sel("request"))
		}
		id, ok := goStringFrom(objc.Send[objc.ID](item, sel("identifier")))
		if !ok {
			return noticeIDs{err: reminders.ErrUnavailable}
		}
		if _, _, owned := reminders.NotificationTaskID(id); owned {
			ids = append(ids, id)
			if !delivered {
				versions[id] = nativeNoticeContentVersion(item)
				trigger := objc.Send[objc.ID](item, sel("trigger"))
				if trigger != 0 && objc.Send[bool](trigger, sel("respondsToSelector:"), sel("nextTriggerDate")) {
					date := objc.Send[objc.ID](trigger, sel("nextTriggerDate"))
					if date != 0 {
						seconds := objc.Send[float64](date, sel("timeIntervalSince1970"))
						if math.IsNaN(seconds) || math.IsInf(seconds, 0) {
							return noticeIDs{err: reminders.ErrUnavailable}
						}
						at := time.Unix(int64(seconds), 0).UTC()
						if !tasks.ValidTime(at) {
							return noticeIDs{err: reminders.ErrUnavailable}
						}
						nextTimes[id] = at
					}
				}
			}
		}
	}
	return noticeIDs{ids: ids, next: nextTimes, versions: versions}
}
func (p *Notifications) list(ctx context.Context, delivered bool) (noticeIDs, error) {
	result := make(chan noticeIDs, 1)
	block := objc.NewBlock(func(_ objc.Block, array objc.ID) {
		withNoticePool(func() { result <- readNoticeIDs(array, delivered) })
	})
	defer block.Release()
	selector := "getPendingNotificationRequestsWithCompletionHandler:"
	if delivered {
		selector = "getDeliveredNotificationsWithCompletionHandler:"
	}
	withNoticePool(func() { objc.Send[objc.ID](p.center, sel(selector), block) })
	select {
	case got := <-result:
		return got, got.err
	case <-ctx.Done():
		return noticeIDs{}, ctx.Err()
	}
}
func (p *Notifications) snapshot(ctx context.Context) (reminders.Snapshot, error) {
	pending, err := p.list(ctx, false)
	if err != nil {
		return reminders.Snapshot{}, err
	}
	delivered, err := p.list(ctx, true)
	if err != nil {
		return reminders.Snapshot{}, err
	}
	return reminders.Snapshot{Pending: pending.ids, Delivered: delivered.ids, NextAt: pending.next, ContentVersions: pending.versions}, nil
}
func (p *Notifications) Snapshot(ctx context.Context) (reminders.Snapshot, error) {
	release, err := p.acquire(ctx)
	if err != nil {
		if err == reminders.ErrUnsupported {
			return reminders.Snapshot{}, nil
		}
		return reminders.Snapshot{}, err
	}
	defer release()
	return p.snapshot(ctx)
}

func noticeRequest(n reminders.Notice) (objc.ID, error) {
	if _, _, ok := reminders.NotificationTaskID(n.ID); !ok || n.Schedule.Validate() != nil || !tasks.ValidTitle(n.Title) {
		return 0, tasks.ErrInvalid
	}
	content := objc.Send[objc.ID](objc.Send[objc.ID](objc.ID(objc.GetClass("UNMutableNotificationContent")), sel("alloc")), sel("init"))
	if content == 0 {
		return 0, reminders.ErrUnavailable
	}
	defer objc.Send[objc.ID](content, sel("release"))
	objc.Send[objc.ID](content, sel("setTitle:"), nsStringFrom("Reminder"))
	// UserNotifications treats percent signs as format escapes in body text;
	// doubling them preserves the intended literal task title on display.
	objc.Send[objc.ID](content, sel("setBody:"), nsStringFrom(strings.ReplaceAll(n.Title, "%", "%%")))
	info := objc.Send[objc.ID](objc.ID(objc.GetClass("NSDictionary")), sel("dictionaryWithObject:forKey:"), nsStringFrom(noticeContentVersionValue), nsStringFrom(noticeContentVersionKey))
	if info == 0 {
		return 0, reminders.ErrUnavailable
	}
	objc.Send[objc.ID](content, sel("setUserInfo:"), info)
	objc.Send[objc.ID](content, sel("setThreadIdentifier:"), nsStringFrom("blur.reminders"))
	sound := objc.Send[objc.ID](objc.ID(objc.GetClass("UNNotificationSound")), sel("defaultSound"))
	objc.Send[objc.ID](content, sel("setSound:"), sound)
	components := objc.Send[objc.ID](objc.Send[objc.ID](objc.ID(objc.GetClass("NSDateComponents")), sel("alloc")), sel("init"))
	if components == 0 {
		return 0, reminders.ErrUnavailable
	}
	defer objc.Send[objc.ID](components, sel("release"))
	r := n.Schedule
	if r.Repeat == tasks.Once && time.Since(r.DueAt) > 30*time.Second {
		return 0, tasks.ErrInvalid
	}
	zoneName := r.Timezone
	if r.Repeat == tasks.Once {
		zoneName = "UTC"
	}
	zone := objc.Send[objc.ID](objc.ID(objc.GetClass("NSTimeZone")), sel("timeZoneWithName:"), nsStringFrom(zoneName))
	if zone == 0 {
		return 0, reminders.ErrUnavailable
	}
	calendar := objc.Send[objc.ID](objc.ID(objc.GetClass("NSCalendar")), sel("calendarWithIdentifier:"), nsStringFrom("gregorian"))
	objc.Send[objc.ID](calendar, sel("setTimeZone:"), zone)
	objc.Send[objc.ID](components, sel("setCalendar:"), calendar)
	objc.Send[objc.ID](components, sel("setTimeZone:"), zone)
	var expected time.Time
	if r.Repeat == tasks.Once {
		expected = r.DueAt.UTC().Truncate(time.Second)
		if expected.Before(r.DueAt) {
			expected = expected.Add(time.Second)
		}
		if !expected.After(time.Now()) {
			return 0, tasks.ErrInvalid
		}
		for _, field := range []struct {
			selector string
			value    int
		}{{"setYear:", expected.Year()}, {"setMonth:", int(expected.Month())}, {"setDay:", expected.Day()}, {"setHour:", expected.Hour()}, {"setMinute:", expected.Minute()}, {"setSecond:", expected.Second()}} {
			objc.Send[objc.ID](components, sel(field.selector), int64(field.value))
		}
	} else {
		expected = r.DueAt
		objc.Send[objc.ID](components, sel("setHour:"), int64(r.Hour))
		objc.Send[objc.ID](components, sel("setMinute:"), int64(r.Minute))
		objc.Send[objc.ID](components, sel("setSecond:"), int64(0))
		if r.Repeat == tasks.Weekly {
			objc.Send[objc.ID](components, sel("setWeekday:"), int64(r.Weekday+1))
		}
	}
	trigger := objc.Send[objc.ID](objc.ID(objc.GetClass("UNCalendarNotificationTrigger")), sel("triggerWithDateMatchingComponents:repeats:"), components, r.Repeat != tasks.Once)
	next := objc.Send[objc.ID](trigger, sel("nextTriggerDate"))
	if next == 0 {
		return 0, reminders.ErrUnavailable
	}
	seconds := objc.Send[float64](next, sel("timeIntervalSince1970"))
	if math.Abs(seconds-float64(expected.Unix())) > 1 {
		return 0, tasks.ErrInvalid
	}
	request := objc.Send[objc.ID](objc.ID(objc.GetClass("UNNotificationRequest")), sel("requestWithIdentifier:content:trigger:"), nsStringFrom(n.ID), content, trigger)
	if request == 0 {
		return 0, reminders.ErrUnavailable
	}
	return request, nil
}

func (p *Notifications) Queue(ctx context.Context, n reminders.Notice) error {
	release, err := p.acquire(ctx)
	if err != nil {
		return err
	}
	result := make(chan error, 1)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var request objc.ID
	withAutoreleasePool(func() {
		request, err = noticeRequest(n)
		if err == nil {
			objc.Send[objc.ID](request, sel("retain"))
		}
	})
	if err != nil {
		release()
		return err
	}
	defer objc.Send[objc.ID](request, sel("release"))
	block := objc.NewBlock(func(_ objc.Block, nativeError objc.ID) {
		var err error
		if nativeError != 0 {
			err = reminders.ErrUnavailable
		}
		result <- err
		release()
	})
	defer block.Release()
	withNoticePool(func() {
		objc.Send[objc.ID](p.center, sel("addNotificationRequest:withCompletionHandler:"), request, block)
	})
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		// Keep the mutation gate until the actual native completion arrives.
		// Later deletion cannot race an unacknowledged add request.
		return reminders.ErrOutcomeUnknown
	}
}

func (p *Notifications) Remove(ctx context.Context, ids []string) error {
	if len(ids) > reminders.MaxSnapshotIDs {
		return tasks.ErrCapacity
	}
	for _, id := range ids {
		if _, _, ok := reminders.NotificationTaskID(id); !ok {
			return tasks.ErrInvalid
		}
	}
	release, err := p.acquire(ctx)
	if err != nil {
		if err == reminders.ErrUnsupported {
			return nil
		}
		return err
	}
	defer release()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	withAutoreleasePool(func() {
		array := objc.Send[objc.ID](objc.ID(objc.GetClass("NSMutableArray")), sel("arrayWithCapacity:"), uint64(len(ids)))
		for _, id := range ids {
			objc.Send[objc.ID](array, sel("addObject:"), nsStringFrom(id))
		}
		objc.Send[objc.ID](p.center, sel("removePendingNotificationRequestsWithIdentifiers:"), array)
		objc.Send[objc.ID](p.center, sel("removeDeliveredNotificationsWithIdentifiers:"), array)
	})
	snapshot, err := p.snapshot(ctx)
	if err != nil {
		return err
	}
	for _, id := range append(snapshot.Pending, snapshot.Delivered...) {
		for _, removed := range ids {
			if id == removed {
				return reminders.ErrUnavailable
			}
		}
	}
	return nil
}
func (p *Notifications) RemoveAll(ctx context.Context) error {
	release, err := p.acquire(ctx)
	if err != nil {
		if err == reminders.ErrUnsupported {
			return nil
		}
		return err
	}
	defer release()
	withNoticePool(func() {
		objc.Send[objc.ID](p.center, sel("removeAllPendingNotificationRequests"))
		objc.Send[objc.ID](p.center, sel("removeAllDeliveredNotifications"))
	})
	snapshot, err := p.snapshot(ctx)
	if err != nil {
		return err
	}
	if len(snapshot.Pending)+len(snapshot.Delivered) != 0 {
		return reminders.ErrUnavailable
	}
	return nil
}

var _ reminders.NotificationPort = (*Notifications)(nil)
