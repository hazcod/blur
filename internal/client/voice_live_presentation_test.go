package client

import (
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/meetings"
)

func TestVoiceRecordingClockCountsOnlyAcknowledgedAudio(t *testing.T) {
	now := time.Date(2026, 11, 13, 21, 0, 0, 0, time.UTC)
	view := MeetingLiveView{}
	if view.RecordingLength(now) != 0 {
		t.Fatal("idle clock advanced")
	}
	view.setAudioRecording(true, now)
	if got := voiceRecordingClock(view.RecordingLength(now.Add(65 * time.Second))); got != "01:05" {
		t.Fatal(got)
	}
	view.setAudioRecording(false, now.Add(65*time.Second))
	if got := view.RecordingLength(now.Add(10 * time.Minute)); got != 65*time.Second {
		t.Fatal("paused time counted as audio", got)
	}
	view.setAudioRecording(true, now.Add(10*time.Minute))
	view.setAudioRecording(false, now.Add(10*time.Minute+5*time.Second))
	if got := voiceRecordingClock(view.RecordingLength(now.Add(time.Hour))); got != "01:10" {
		t.Fatal("resume lost elapsed audio", got)
	}
	if got := voiceRecordingClock(time.Hour + 2*time.Minute + 3*time.Second); got != "1:02:03" {
		t.Fatal(got)
	}
	if got := voiceRecordingClock(-time.Second); got != "00:00" {
		t.Fatal(got)
	}
}

func TestVoiceLivePhraseIsOneBoundedCurrentPhrase(t *testing.T) {
	view := MeetingLiveView{LivePhrase: "Current\nspoken phrase", Preview: map[string]string{"microphone": "Older preview"}}
	if got := voiceLivePhrase(view); got != "Current spoken phrase" {
		t.Fatal(got)
	}
	view.LivePhrase = strings.Repeat("Long phrase ", 100)
	if got := voiceLivePhrase(view); len([]rune(got)) > 120 || strings.Contains(got, "\n") {
		t.Fatal("unbounded live phrase", got)
	}
	view = MeetingLiveView{Tail: []meetings.StoredRecord{{Record: meetings.Record{Kind: meetings.TranscriptFinal, Event: meetings.Event{Text: "Last saved spoken phrase"}}}}}
	if got := voiceLivePhrase(view); got != "" {
		t.Fatal("saved history reappeared as the live phrase", got)
	}
	for _, state := range []string{"pausing", "paused"} {
		view = MeetingLiveView{State: state, LivePhrase: "Old spoken phrase", Preview: map[string]string{"microphone": "Old preview"}}
		if got := voiceLivePhrase(view); got != "" {
			t.Fatal("paused recording retained spoken words", got)
		}
	}
}
