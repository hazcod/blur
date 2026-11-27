package assistant

import (
	"testing"
	"time"
)

func TestDutchSaidQuestionKeepsRecordingObservationDate(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	for _, question := range []string{"Wanneer zei ik in mijn spraaknotitie iets over Cedar gisteren?", "Hoe laat zei ik in mijn spraaknotitie iets over Cedar gisteren?"} {
		p, err := PlanQuestion(question, now, nil)
		if err != nil || p.EventTime || !p.Scope.Start.Equal(dayStart(now, -1)) || !p.Scope.End.Equal(dayStart(now, 0)) {
			t.Fatal("speech activity became event time", p, err)
		}
	}
}
