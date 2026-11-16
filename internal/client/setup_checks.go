package client

import (
	"context"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/platform/darwin"
	"github.com/hazcod/blur/internal/reminders"
)

type setupChecksState struct {
	ctx                    context.Context
	initialized            bool
	voiceChecking          bool
	voiceCancel            context.CancelFunc
	voiceRequest           uint64
	voiceLocale            string
	voiceState             string
	voiceIssue             string
	voiceCheckedAt         time.Time
	downloading            bool
	notificationChecking   bool
	notificationRequesting bool
	notification           reminders.Authorization
	notificationState      string
	notificationIssue      string
	notificationCheckedAt  time.Time
}

// ReminderAuthorization uses the reminder service's existing notification port;
// unlike RequestReminderAuthorization it cannot show a permission prompt.
func (c *Client) ReminderAuthorization(parent context.Context) (reminders.Authorization, error) {
	if c == nil || c.reminders == nil {
		return reminders.Unsupported, ErrNotConfigured
	}
	ctx, done, err := c.beginOperation(parent, 5*time.Second)
	if err != nil {
		return "", err
	}
	defer done()
	return c.reminders.Authorization(ctx)
}

func (d *desktop) initializeSetupChecks(done <-chan struct{}) {
	if d == nil || d.client == nil || d.setupChecks.initialized {
		return
	}
	base := d.client.lifeCtx
	if base == nil {
		base = context.Background()
	}
	d.setupChecks.ctx = base
	d.setupChecks.initialized = true
	if done != nil {
		ctx, cancel := context.WithCancel(base)
		d.setupChecks.ctx = ctx
		go func() {
			defer cancel()
			select {
			case <-done:
			case <-ctx.Done():
			}
		}()
	}
	if d.setupChecksVisible() {
		d.refreshSetupChecks()
	}
}
func (d *desktop) setupChecksVisible() bool {
	return d != nil && (d.setupRequired || (d.dashboardOpen && d.page == "Settings" && normalSettingsGroup(d.settingsGroup) == settingsGroupCapture))
}
func (d *desktop) setupChecksContext() context.Context {
	if d.setupChecks.ctx != nil {
		return d.setupChecks.ctx
	}
	return context.Background()
}
func (d *desktop) voiceSetupDownloading() bool { return d != nil && d.setupChecks.downloading }
func (d *desktop) refreshSetupChecks() {
	d.refreshVoiceSetupCheck(false)
	d.refreshNotificationSetupCheck(false)
}
func (d *desktop) refreshSetupChecksNow() {
	d.refreshVoiceSetupCheck(true)
	d.refreshNotificationSetupCheck(true)
}

func (d *desktop) refreshVoiceSetupCheck(force bool) {
	if d == nil || d.client == nil || d.app == nil || !d.setupChecksVisible() || d.setupChecksContext().Err() != nil || d.voiceSetupDownloading() || d.meetingUI.busy || d.meetingUI.probing || d.meetingUI.live.AudioRecording || voiceRecordingActive(d.meetingUI.live.State) {
		return
	}
	u := &d.setupChecks
	locale := d.savedVoiceNotesSettings().Locale
	if u.voiceChecking && u.voiceLocale == locale {
		return
	}
	if !force && u.voiceLocale == locale && !u.voiceCheckedAt.IsZero() && time.Since(u.voiceCheckedAt) < 30*time.Second {
		return
	}
	if u.voiceCancel != nil {
		u.voiceCancel()
	}
	ctx, cancel := context.WithTimeout(d.setupChecksContext(), 3*time.Second)
	u.voiceCancel = cancel
	u.voiceRequest++
	request := u.voiceRequest
	u.voiceLocale = locale
	u.voiceChecking = true
	u.voiceState = "checking"
	u.voiceIssue = ""
	d.app.RequestUpdate()
	go func() {
		err := d.client.ProbeMeetings(ctx, locale)
		availability := d.client.MeetingLive().Availability
		cancel()
		d.app.Post(func() {
			if u.voiceRequest != request {
				return
			}
			u.voiceChecking = false
			u.voiceCancel = nil
			if locale != d.savedVoiceNotesSettings().Locale {
				u.voiceCheckedAt = time.Time{}
				d.refreshVoiceSetupCheck(false)
				return
			}
			d.publishVoiceSetupCheck(locale, availability, err)
			d.app.RequestUpdate()
		})
	}()
}
func (d *desktop) publishVoiceSetupCheck(locale string, availability darwin.MeetingAudioAvailability, err error) {
	u := &d.setupChecks
	u.voiceLocale = locale
	u.voiceCheckedAt = time.Now()
	u.voiceIssue = ""
	if err != nil {
		u.voiceState = "unavailable"
		u.voiceIssue = "Speech readiness could not be checked. Try again."
		return
	}
	if !availability.SpeechAvailable {
		u.voiceState = "unavailable"
		u.voiceIssue = "Apple on-device speech transcription is unavailable on this Mac or build."
		return
	}
	// InstalledLocales belongs to the whole device. Do not borrow a different
	// Ready recording's requested-locale bit if another probe updated the view.
	for _, installed := range availability.InstalledLocales {
		if strings.EqualFold(installed, locale) {
			u.voiceState = "ready"
			if d.meetingUI.locale == locale {
				availability.RequestedLocaleInstalled = true
				availability.InstalledLocales = append([]string(nil), availability.InstalledLocales...)
				d.meetingUI.live.Availability = availability
				if d.meetingUI.setupIssue == "assets_missing" {
					d.meetingUI.setupIssue = ""
				}
			}
			return
		}
	}
	u.voiceState = "download_needed"
}

func (d *desktop) downloadSetupVoiceLanguage() {
	if d == nil || d.client == nil || d.app == nil || d.setupChecks.downloading || d.setupChecks.voiceChecking || d.setupChecks.voiceState != "download_needed" || d.meetingUI.busy || d.meetingUI.probing || d.meetingUI.live.AudioRecording || voiceRecordingActive(d.meetingUI.live.State) || d.setupChecksContext().Err() != nil {
		return
	}
	locale := d.savedVoiceNotesSettings().Locale
	if d.setupChecks.voiceLocale != locale {
		d.refreshVoiceSetupCheck(true)
		return
	}
	u := &d.setupChecks
	u.downloading = true
	u.voiceState = "downloading"
	u.voiceIssue = ""
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(d.setupChecksContext(), 5*time.Minute)
		err := d.client.InstallMeetingLanguage(ctx, locale)
		var availability darwin.MeetingAudioAvailability
		if err == nil {
			probe, stop := context.WithTimeout(ctx, 3*time.Second)
			err = d.client.ProbeMeetings(probe, locale)
			availability = d.client.MeetingLive().Availability
			stop()
		}
		cancel()
		d.app.Post(func() {
			u.downloading = false
			if locale != d.savedVoiceNotesSettings().Locale {
				u.voiceCheckedAt = time.Time{}
				u.voiceIssue = ""
				d.refreshVoiceSetupCheck(false)
				d.app.RequestUpdate()
				return
			}
			if err != nil {
				u.voiceState = "download_needed"
				u.voiceIssue = "Speech files could not be downloaded or verified. Try again."
				u.voiceCheckedAt = time.Time{}
			} else {
				d.publishVoiceSetupCheck(locale, availability, nil)
				if u.voiceState == "download_needed" {
					u.voiceIssue = "Speech files are not ready yet. You can retry the download or check again."
				}
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) refreshNotificationSetupCheck(force bool) {
	if d == nil || d.client == nil || d.app == nil || !d.setupChecksVisible() || d.setupChecksContext().Err() != nil {
		return
	}
	u := &d.setupChecks
	if u.notificationChecking || u.notificationRequesting || (!force && !u.notificationCheckedAt.IsZero() && time.Since(u.notificationCheckedAt) < 30*time.Second) {
		return
	}
	u.notificationChecking = true
	u.notificationState = "checking"
	u.notificationIssue = ""
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(d.setupChecksContext(), 5*time.Second)
		auth, err := d.client.ReminderAuthorization(ctx)
		cancel()
		d.app.Post(func() {
			u.notificationChecking = false
			d.publishNotificationSetupCheck(auth, err)
			d.app.RequestUpdate()
		})
	}()
}
func (d *desktop) publishNotificationSetupCheck(auth reminders.Authorization, err error) {
	u := &d.setupChecks
	u.notificationCheckedAt = time.Now()
	u.notificationIssue = ""
	if err != nil {
		u.notification = ""
		u.notificationState = "unavailable"
		u.notificationIssue = "Notification permission could not be checked. Your notes and tasks still work."
		return
	}
	u.notification = auth
	switch auth {
	case reminders.Allowed:
		u.notificationState = "allowed"
	case reminders.Denied:
		u.notificationState = "denied"
	case reminders.NotDetermined:
		u.notificationState = "not_asked"
	default:
		u.notificationState = "unavailable"
	}
}
func (d *desktop) requestSetupNotifications() {
	if d == nil || d.client == nil || d.app == nil || d.setupChecks.notificationRequesting || d.setupChecks.notificationChecking || d.setupChecks.notification != reminders.NotDetermined || d.setupChecksContext().Err() != nil {
		return
	}
	u := &d.setupChecks
	u.notificationRequesting = true
	u.notificationIssue = "Waiting for your choice in macOS…"
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(d.setupChecksContext(), 90*time.Second)
		auth, err := d.client.RequestReminderAuthorization(ctx)
		cancel()
		d.app.Post(func() {
			u.notificationRequesting = false
			d.publishNotificationSetupCheck(auth, err)
			d.app.RequestUpdate()
		})
	}()
}
func (d *desktop) openSetupNotificationSettings() {
	if d == nil || d.app == nil {
		return
	}
	if err := openNotificationSettings(); err != nil {
		d.setupChecks.notificationIssue = "System Settings could not be opened. Open Notifications → Blur in macOS System Settings."
	} else {
		d.setupChecks.notificationIssue = "Change Blur's notification access in System Settings, then return and check again."
	}
	d.app.RequestUpdate()
}
