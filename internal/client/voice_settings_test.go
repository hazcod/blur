package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/preferences"
)

func voicePreferencesDesktop(t *testing.T) (*desktop, *preferences.Store, string, *meetingOperationTestApp) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := preferences.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	config := preferences.DefaultConfig()
	config.CaptureEnabled = false
	config.ClaudeMCPEnabled = true
	config.ExcludedApps = []string{"com.example.private"}
	if err := store.Save(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	app := &meetingOperationTestApp{posted: make(chan func(), 8)}
	c := NewClient(&testData{}).WithPreferences(store)
	t.Cleanup(c.Close)
	return &desktop{app: app, client: c, preferences: config}, store, path, app
}

func TestVoiceSettingsSaveIsAsyncMergesLatestAndAppliesOnlyAfterPersistence(t *testing.T) {
	d, store, _, app := voicePreferencesDesktop(t)
	d.applySavedVoiceNotesSettings()
	d.ensureVoiceSettingsDraft()
	d.setVoiceSettingsSource(preferences.VoiceSourceSystem)
	d.setVoiceSettingsLocale("nl-be")
	latest := d.preferences
	latest.IncludedDomains = []string{"work.test"}
	latest.LogLevel = "debug"
	if err := store.Save(context.Background(), latest); err != nil {
		t.Fatal(err)
	}
	entered, release, unlocked := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	go func() {
		_ = store.WithConfig(context.Background(), func(preferences.Config) error { close(entered); <-release; return nil })
		close(unlocked)
	}()
	<-entered
	returned := make(chan struct{})
	go func() { d.saveVoiceSettings(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		unblock()
		<-returned
		t.Fatal("voice save blocked UI behind capture admission")
	}
	if !d.voiceSettings.saving || !d.loading || d.meetingUI.locale != "en-US" || !d.meetingUI.microphone {
		t.Fatal("pending save changed recording settings or has no saving state")
	}
	unblock()
	<-unlocked
	app.finish(t)
	if d.voiceSettings.saving || d.loading || d.voiceSettings.status != "Saved for the next new recording." {
		t.Fatalf("save receipt = %+v", d.voiceSettings)
	}
	if d.preferences.VoiceNotes.Source != preferences.VoiceSourceSystem || d.preferences.VoiceNotes.Locale != "nl-BE" || d.preferences.CaptureEnabled || !d.preferences.ClaudeMCPEnabled || d.preferences.LogLevel != "debug" || !reflect.DeepEqual(d.preferences.IncludedDomains, []string{"work.test"}) {
		t.Fatalf("saved choices clobbered latest preferences: %+v", d.preferences)
	}
	if d.meetingUI.microphone || !d.meetingUI.system || d.meetingUI.locale != "nl-BE" {
		t.Fatal("saved choices not applied to next recording")
	}
}

func TestVoiceSettingsFailurePreservesSavedChoicesAndRetryDraft(t *testing.T) {
	d, store, path, app := voicePreferencesDesktop(t)
	d.applySavedVoiceNotesSettings()
	d.ensureVoiceSettingsDraft()
	d.setVoiceSettingsSource(preferences.VoiceSourceMicrophone)
	d.setVoiceSettingsLocale("fr-FR")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	d.saveVoiceSettings()
	app.finish(t)
	if d.voiceSettings.saving || d.loading || d.voiceSettings.status != "Voice settings could not be saved. Your saved choices are unchanged. Try again." || d.preferences.VoiceNotes != preferences.DefaultVoiceNotesConfig() {
		t.Fatalf("misleading failed receipt: %+v prefs=%+v", d.voiceSettings, d.preferences)
	}
	if d.voiceSettings.draft.Locale != "fr-FR" || !d.meetingUI.system {
		t.Fatal("failed save lost draft or changed recording")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	d.saveVoiceSettings()
	app.finish(t)
	got, err := store.Load(context.Background())
	if err != nil || got.VoiceNotes.Locale != "fr-FR" || got.VoiceNotes.Source != preferences.VoiceSourceMicrophone {
		t.Fatalf("retry = %+v %v", got, err)
	}
}

func TestVoiceSettingsLockActiveStatesAndPreserveReadyMetadata(t *testing.T) {
	for _, state := range []string{"starting", "running", "pausing", "paused", "stopping"} {
		t.Run(state, func(t *testing.T) {
			d, _, _, _ := voicePreferencesDesktop(t)
			d.ensureVoiceSettingsDraft()
			d.meetingUI.live.State = state
			before := d.voiceSettings.draft
			d.setVoiceSettingsLocale("fr-FR")
			d.setVoiceSettingsSource(preferences.VoiceSourceSystem)
			d.saveVoiceSettings()
			if d.voiceSettings.draft != before || d.loading || d.voiceSettingsEditable() {
				t.Fatal("active recording settings editable")
			}
		})
	}
	d, _, _, app := voicePreferencesDesktop(t)
	d.meetingUI.selectedState = meetings.Ready
	d.meetingUI.locale = "ja-JP"
	d.meetingUI.microphone = true
	d.ensureVoiceSettingsDraft()
	d.setVoiceSettingsLocale("fr-FR")
	d.saveVoiceSettings()
	app.finish(t)
	if d.preferences.VoiceNotes.Locale != "fr-FR" || d.meetingUI.locale != "ja-JP" || !d.meetingUI.microphone || d.meetingUI.system {
		t.Fatal("saved preferences overwrote Ready agenda metadata")
	}
	for _, busy := range []bool{true, false} {
		d.meetingUI.busy = busy
		d.meetingUI.probing = !busy
		if d.voiceSettingsEditable() {
			t.Fatal("voice operation permits selector edits")
		}
	}
}

func TestVoiceSettingsValidationAndViewNeverReadSettingsFile(t *testing.T) {
	d, _, path, _ := voicePreferencesDesktop(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if d.voiceSettingsView() == nil {
		t.Fatal("missing Voice settings card")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("view created/read settings through persistence")
	}
	d.setVoiceSettingsLocale("../bad")
	d.saveVoiceSettings()
	if d.loading || d.voiceSettings.saving || d.voiceSettings.status != "Use a language code such as en-US, nl-BE, or fr-FR." {
		t.Fatalf("invalid locale not explained: %+v", d.voiceSettings)
	}
	if d.preferences.VoiceNotes.Locale != "en-US" {
		t.Fatal("invalid input changed saved locale")
	}
}

func TestVoiceSettingsSaveFinishingDuringRecordingDoesNotChangeIt(t *testing.T) {
	d, store, _, app := voicePreferencesDesktop(t)
	d.applySavedVoiceNotesSettings()
	d.ensureVoiceSettingsDraft()
	d.setVoiceSettingsLocale("nl-BE")
	d.setVoiceSettingsSource(preferences.VoiceSourceSystem)
	entered, release, unlocked := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	go func() {
		_ = store.WithConfig(context.Background(), func(preferences.Config) error { close(entered); <-release; return nil })
		close(unlocked)
	}()
	<-entered
	d.saveVoiceSettings()
	d.meetingUI.live.State = "running"
	d.meetingUI.live.AudioRecording = true
	d.meetingUI.locale = "ja-JP"
	d.meetingUI.microphone = true
	d.meetingUI.system = false
	unblock()
	<-unlocked
	app.finish(t)
	if d.preferences.VoiceNotes.Locale != "nl-BE" || d.meetingUI.locale != "ja-JP" || !d.meetingUI.microphone || d.meetingUI.system {
		t.Fatal("late save changed active recording metadata")
	}
}

func TestVoiceSettingsDraftFollowsSavedSnapshotWithoutLosingEdits(t *testing.T) {
	d, _, _, _ := voicePreferencesDesktop(t)
	d.ensureVoiceSettingsDraft()
	d.preferences.VoiceNotes.Locale = "nl-BE"
	d.ensureVoiceSettingsDraft()
	if d.voiceSettings.draft.Locale != "nl-BE" {
		t.Fatal("pristine draft ignored refreshed saved preference")
	}
	d.setVoiceSettingsLocale("fr-FR")
	d.preferences.VoiceNotes.Locale = "ja-JP"
	d.ensureVoiceSettingsDraft()
	if d.voiceSettings.draft.Locale != "fr-FR" {
		t.Fatal("snapshot refresh lost unsaved edit")
	}
	d.meetingUI.live.State = "failed"
	d.meetingUI.live.AudioRecording = true
	if d.voiceSettingsEditable() {
		t.Fatal("recording acknowledgement still active despite failed status")
	}
}

func TestConfigureVoiceNotesHonorsClientClose(t *testing.T) {
	d, store, _, _ := voicePreferencesDesktop(t)
	d.client.Close()
	if _, err := d.client.ConfigureVoiceNotes(context.Background(), preferences.VoiceNotesConfig{Source: preferences.VoiceSourceSystem, Locale: "nl-BE"}); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("closed voice settings update = %v", err)
	}
	got, err := store.Load(context.Background())
	if err != nil || got.VoiceNotes != preferences.DefaultVoiceNotesConfig() {
		t.Fatal("closed client changed voice preferences")
	}
}
