package darwin

import (
	"errors"
	"strings"
	"testing"
)

func TestMeetingAudioLevelSchemaIsFiniteContentFreeAndSeparateFromTranscript(t *testing.T) {
	fixture := `{"version":1,"session_id":"synthetic","events":[{"version":1,"session_id":"synthetic","epoch":1,"kind":"level","track":"microphone","level":0.5}]}`
	for _, level := range []string{"0", "0.5", "1"} {
		events, err := decodeMeetingAudioBatch([]byte(strings.Replace(fixture, `"level":0.5`, `"level":`+level, 1)), "synthetic")
		if err != nil || len(events) != 1 || events[0].Level == nil || *events[0].Level < 0 || *events[0].Level > 1 {
			t.Fatalf("valid normalized level rejected: %v", err)
		}
	}
	for _, data := range []string{
		strings.Replace(fixture, `,"level":0.5`, "", 1), strings.Replace(fixture, `"level":0.5`, `"level":null`, 1), strings.Replace(fixture, `"level":0.5`, `"level":-0.01`, 1), strings.Replace(fixture, `"level":0.5`, `"level":1.01`, 1), strings.Replace(fixture, `"level":0.5`, `"level":1e309`, 1), strings.Replace(fixture, `"level":0.5`, `"level":"NaN"`, 1), strings.Replace(fixture, `"level":0.5`, `"level":true`, 1),
		strings.Replace(fixture, `"track":"microphone"`, `"track":"person name"`, 1), strings.Replace(fixture, `"epoch":1`, `"epoch":0`, 1), strings.Replace(fixture, `"level":0.5`, `"level":0.5,"text":"secret words"`, 1), strings.Replace(fixture, `"level":0.5`, `"level":0.5,"start_ms":0`, 1), strings.Replace(fixture, `"level":0.5`, `"level":0.5,"final":true`, 1), strings.Replace(fixture, `"level":0.5`, `"level":0.5,"segment_id":"one"`, 1), strings.Replace(meetingAudioFinalFixture, `"kind":"transcript"`, `"kind":"transcript","level":0.5`, 1),
	} {
		if _, err := decodeMeetingAudioBatch([]byte(data), "synthetic"); !errors.Is(err, ErrMeetingAudioInvalid) {
			t.Fatal("unsafe or content-bearing level accepted")
		}
	}
}
