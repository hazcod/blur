package client

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hazcod/blur/internal/platform/darwin"
	"github.com/hazcod/blur/internal/reminders"
)

type setupAudioFixture struct {
	controllerAudioFixture
	mu                                  sync.Mutex
	installed                           bool
	speech                              bool
	installReady                        bool
	downloadError                       error
	probeError                          error
	probes, downloads, starts, requests atomic.Int32
	blocked                             chan struct{}
}

func (a *setupAudioFixture) Probe(ctx context.Context, locale string) (darwin.MeetingAudioAvailability, error) {
	a.probes.Add(1)
	if a.blocked != nil {
		select {
		case <-a.blocked:
		case <-ctx.Done():
			return darwin.MeetingAudioAvailability{}, ctx.Err()
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	v := darwin.MeetingAudioAvailability{SpeechAvailable: a.speech, RequestedLocaleInstalled: a.installed}
	if a.installed {
		v.InstalledLocales = []string{locale}
	}
	return v, a.probeError
}
func (a *setupAudioFixture) InstallLocale(context.Context, string) error {
	a.downloads.Add(1)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.downloadError != nil {
		return a.downloadError
	}
	a.installed = a.installReady
	return nil
}
func (a *setupAudioFixture) StartMeeting(context.Context, darwin.MeetingAudioStart) (meetingAudioStream, error) {
	a.starts.Add(1)
	return nil, errors.New("recording forbidden in setup")
}
func (a *setupAudioFixture) RequestAccess(context.Context, []string) error {
	a.requests.Add(1)
	return errors.New("audio access forbidden in setup")
}

type setupNoticeFixture struct {
	reminders.UnavailablePort
	auth           reminders.Authorization
	readError      error
	reads, prompts atomic.Int32
}

func (p *setupNoticeFixture) Authorization(ctx context.Context) (reminders.Authorization, error) {
	p.reads.Add(1)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return p.auth, p.readError
}
func (p *setupNoticeFixture) RequestAuthorization(context.Context) (reminders.Authorization, error) {
	p.prompts.Add(1)
	return reminders.Allowed, nil
}

func setupChecklistDesktop(t *testing.T) (*desktop, *setupAudioFixture, *setupNoticeFixture, *meetingOperationTestApp) {
	t.Helper()
	f := newControllerFixture(t)
	audio := &setupAudioFixture{speech: true}
	f.c.meetings.open = func() (meetingAudio, error) { return audio, nil }
	notice := &setupNoticeFixture{auth: reminders.NotDetermined}
	service, err := reminders.NewService(f.store, notice, reminders.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	f.c.reminders = service
	app := &meetingOperationTestApp{posted: make(chan func(), 16)}
	d := &desktop{client: f.c, app: app, page: "Settings", dashboardOpen: true, settingsGroup: settingsGroupCapture}
	return d, audio, notice, app
}

func TestSetupChecklistReadOnlyChecksAreAsyncCoalescedAndDoNotRecord(t *testing.T) {
	d, a, p, app := setupChecklistDesktop(t)
	a.blocked = make(chan struct{})
	d.refreshSetupChecksNow()
	d.refreshSetupChecksNow()
	if !d.setupChecks.voiceChecking || !d.setupChecks.notificationChecking {
		t.Fatal("checks did not start asynchronously")
	}
	app.finish(t) // Notification completes while speech is still waiting.
	close(a.blocked)
	app.finish(t)
	if d.setupChecks.voiceState != "download_needed" || d.setupChecks.notification != reminders.NotDetermined || a.probes.Load() != 1 || p.reads.Load() != 1 || p.prompts.Load() != 0 || a.starts.Load() != 0 || a.requests.Load() != 0 {
		t.Fatalf("readiness checks wrong/side effects: %+v audio=%d/%d/%d notifications=%d/%d", d.setupChecks, a.probes.Load(), a.starts.Load(), a.requests.Load(), p.reads.Load(), p.prompts.Load())
	}
	d.refreshSetupChecks()
	if d.setupChecks.voiceChecking || d.setupChecks.notificationChecking {
		t.Fatal("cached checklist rechecked without throttle")
	}
}

func TestSetupVoiceDownloadRequiresClickAndRechecksActualReadiness(t *testing.T) {
	for _, ready := range []bool{true, false} {
		t.Run(map[bool]string{true: "ready", false: "pending"}[ready], func(t *testing.T) {
			d, a, _, app := setupChecklistDesktop(t)
			a.installReady = ready
			d.refreshSetupChecksNow()
			app.finish(t)
			app.finish(t)
			if a.downloads.Load() != 0 {
				t.Fatal("passive setup downloaded speech assets")
			}
			d.downloadSetupVoiceLanguage()
			if !d.voiceSetupDownloading() {
				t.Fatal("explicit download has no pending state")
			}
			app.finish(t)
			want := "download_needed"
			if ready {
				want = "ready"
			}
			if d.setupChecks.voiceState != want || a.downloads.Load() != 1 || a.starts.Load() != 0 || a.requests.Load() != 0 || d.voiceSetupDownloading() {
				t.Fatalf("download reported false readiness or started audio: %+v", d.setupChecks)
			}
		})
	}
}

func TestSetupNotificationPermissionIsExplicitAndFailureIsVisible(t *testing.T) {
	d, _, p, app := setupChecklistDesktop(t)
	d.refreshSetupChecksNow()
	app.finish(t)
	app.finish(t)
	if p.prompts.Load() != 0 {
		t.Fatal("check requested notification permission")
	}
	d.requestSetupNotifications()
	app.finish(t)
	if p.prompts.Load() != 1 || d.setupChecks.notification != reminders.Allowed {
		t.Fatalf("explicit request = %+v prompts=%d", d.setupChecks, p.prompts.Load())
	}
	p.readError = errors.New("synthetic failure")
	d.refreshSetupChecksNow()
	app.finish(t)
	app.finish(t)
	if d.setupChecks.notificationState != "unavailable" || d.setupChecks.notificationIssue == "" {
		t.Fatal("failed check left false allowed state")
	}
}

func TestSetupVoiceFailureAndActiveRecordingDoNotDownload(t *testing.T) {
	d, a, _, app := setupChecklistDesktop(t)
	a.probeError = errors.New("synthetic failure")
	d.refreshSetupChecksNow()
	app.finish(t)
	app.finish(t)
	if d.setupChecks.voiceState != "unavailable" || d.setupChecks.voiceIssue == "" {
		t.Fatal("speech check failure hidden")
	}
	d.downloadSetupVoiceLanguage()
	if a.downloads.Load() != 0 {
		t.Fatal("failed check admitted download")
	}
	d.setupChecks.voiceState = "download_needed"
	d.meetingUI.live.State = "running"
	d.downloadSetupVoiceLanguage()
	if d.voiceSetupDownloading() || a.downloads.Load() != 0 {
		t.Fatal("recording permits setup download")
	}
}

func TestSetupInitializeDoesNotProbeOutsideVisibleChecklistAndCloseCancels(t *testing.T) {
	d, a, p, _ := setupChecklistDesktop(t)
	d.page = "Today"
	done := make(chan struct{})
	d.initializeSetupChecks(done)
	if a.probes.Load() != 0 || p.reads.Load() != 0 || d.setupChecks.voiceChecking {
		t.Fatal("startup probed audio outside setup")
	}
	close(done)
	<-d.setupChecks.ctx.Done()
	d.page = "Settings"
	d.refreshSetupChecksNow()
	if a.probes.Load() != 0 || p.reads.Load() != 0 {
		t.Fatal("closed checklist admitted a new check")
	}
}

func TestSetupVerifiedLanguageClearsOnlyMatchingMissingAssetIssue(t *testing.T) {
	d, _, _, _ := setupChecklistDesktop(t)
	d.meetingUI.locale = "en-US"
	d.meetingUI.setupIssue = "assets_missing"
	availability := darwin.MeetingAudioAvailability{SpeechAvailable: true, InstalledLocales: []string{"en-US"}}
	d.publishVoiceSetupCheck("en-US", availability, nil)
	if d.meetingUI.setupIssue != "" || !d.meetingUI.live.Availability.RequestedLocaleInstalled {
		t.Fatal("verified download left stale missing-language state")
	}
	availability.InstalledLocales[0] = "fr-FR"
	if d.meetingUI.live.Availability.InstalledLocales[0] != "en-US" {
		t.Fatal("live availability aliases caller slice")
	}
	d.meetingUI.locale = "fr-FR"
	d.meetingUI.setupIssue = "assets_missing"
	d.publishVoiceSetupCheck("en-US", darwin.MeetingAudioAvailability{SpeechAvailable: true, InstalledLocales: []string{"en-US"}}, nil)
	if d.meetingUI.setupIssue != "assets_missing" {
		t.Fatal("different Ready locale's issue cleared")
	}
	d.meetingUI.locale = "en-US"
	d.meetingUI.setupIssue = "microphone_permission"
	d.publishVoiceSetupCheck("en-US", darwin.MeetingAudioAvailability{SpeechAvailable: true, InstalledLocales: []string{"en-US"}}, nil)
	if d.meetingUI.setupIssue != "microphone_permission" {
		t.Fatal("speech verification cleared unrelated permission failure")
	}
}

func TestSetupHiddenSettingsDoNotProbeAndViewIsPure(t *testing.T) {
	d, a, p, _ := setupChecklistDesktop(t)
	d.dashboardOpen = false
	d.refreshSetupChecksNow()
	if d.setupChecks.voiceChecking || d.setupChecks.notificationChecking || a.probes.Load() != 0 || p.reads.Load() != 0 {
		t.Fatal("hidden Settings started a readiness check")
	}
	if d.setupChecksView() == nil || a.probes.Load() != 0 || p.reads.Load() != 0 || p.prompts.Load() != 0 {
		t.Fatal("rendering checklist performed native work")
	}
}

func TestSetupDownloadFailureIsVisibleWithoutRecording(t *testing.T) {
	d, a, _, app := setupChecklistDesktop(t)
	a.downloadError = errors.New("synthetic installation failure")
	d.refreshSetupChecksNow()
	app.finish(t)
	app.finish(t)
	d.downloadSetupVoiceLanguage()
	app.finish(t)
	if d.setupChecks.voiceState != "download_needed" || d.setupChecks.voiceIssue == "" || d.voiceSetupDownloading() || a.starts.Load() != 0 || a.requests.Load() != 0 {
		t.Fatal("failed download left false ready/busy/recording state")
	}
}
