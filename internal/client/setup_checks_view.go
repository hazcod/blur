package client

import (
	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
)

func (d *desktop) setupChecksView() ui.View {
	u := &d.setupChecks
	rows := []ui.View{wrapLabel("Setup checks").Style(styleSectionTitle), wrapLabel("Voice notes and reminder notifications are optional. Ask, Search, and saved notes work independently.").Style(modern.MutedText)}
	accessibility := "Accessibility · checked when capture starts"
	switch d.captureState {
	case "permission_required":
		accessibility = "Accessibility · permission needed"
	case "available", "capturing":
		accessibility = "Accessibility · available"
	case "paused":
		accessibility = "Accessibility · capture paused"
	case "unavailable":
		accessibility = "Accessibility · unavailable"
	}
	rows = append(rows, wrapLabel(accessibility))
	if supportsAccessibilitySettingsShortcut() && d.captureState == "permission_required" {
		rows = append(rows, flatButton("Open Accessibility Settings").Enabled(!d.loading).OnClick(d.openAccessibilitySettings))
	}
	locale := d.savedVoiceNotesSettings().Locale
	voice := "Speech language (" + locale + ") · not checked"
	if u.voiceLocale == locale {
		switch u.voiceState {
		case "checking":
			voice = "Speech language (" + locale + ") · checking…"
		case "ready":
			voice = "Speech language (" + locale + ") · downloaded and ready"
		case "download_needed":
			voice = "Speech language (" + locale + ") · download needed"
		case "downloading":
			voice = "Speech language (" + locale + ") · downloading…"
		case "unavailable":
			voice = "Speech language (" + locale + ") · unavailable"
		}
	}
	rows = append(rows, wrapLabel(voice))
	voiceIdle := !d.meetingUI.busy && !d.meetingUI.probing && !d.meetingUI.live.AudioRecording && !voiceRecordingActive(d.meetingUI.live.State)
	if u.voiceLocale == locale && u.voiceState == "download_needed" {
		rows = append(rows, primaryButton("Download speech language").Enabled(voiceIdle && !u.downloading && !u.voiceChecking).OnClick(d.downloadSetupVoiceLanguage), wrapLabel("Downloads Apple speech files for on-device transcription. This does not start recording or request microphone access.").Style(modern.MutedText))
	}
	if u.voiceIssue != "" {
		rows = append(rows, wrapLabel(u.voiceIssue).Style(modern.MutedText))
	}
	notification := "Notifications · not checked"
	switch u.notificationState {
	case "checking":
		notification = "Notifications · checking…"
	case "allowed":
		notification = "Notifications · allowed"
	case "denied":
		notification = "Notifications · denied"
	case "not_asked":
		notification = "Notifications · not asked yet"
	case "unavailable":
		notification = "Notifications · unavailable"
	}
	rows = append(rows, wrapLabel(notification))
	if u.notificationState == "not_asked" {
		rows = append(rows, primaryButton("Allow notifications").Enabled(!u.notificationRequesting && !u.notificationChecking).OnClick(d.requestSetupNotifications))
	}
	if u.notificationState == "denied" {
		rows = append(rows, flatButton("Open Notification Settings").Enabled(notificationSettingsSupported()).OnClick(d.openSetupNotificationSettings), wrapLabel("Reminders remain saved; notification delivery needs permission in macOS.").Style(modern.MutedText))
	}
	if u.notificationIssue != "" {
		rows = append(rows, wrapLabel(u.notificationIssue).Style(modern.MutedText))
	}
	rows = append(rows, flatButton("Check again").Enabled(!u.voiceChecking && !u.notificationChecking && !u.downloading && !u.notificationRequesting).OnClick(d.refreshSetupChecksNow))
	return surfaceBox(14, 9, rows...)
}
