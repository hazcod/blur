package intent

import "testing"

func TestDutchTaskCollectionReadsKeepLocalFilters(t *testing.T) {
	for _, tc := range []struct {
		input, state, window string
		reminders, count     bool
	}{
		{"Welke taken staan nog open?", "open", "", false, false},
		{"Toon mijn afgeronde taken van gisteren", "completed", "yesterday", false, false},
		{"Welke herinneringen heb ik morgen?", "open", "tomorrow", true, false},
		{"Hoeveel taken heb ik nog?", "open", "", false, true},
		{"Wat moet ik nog doen?", "open", "", false, false},
		{"Toon mijn geannuleerde taken van vorige week", "cancelled", "last week", false, false},
	} {
		got, ok := TaskListRequest(tc.input)
		if !ok || !got.Generic || got.State != tc.state || got.Window != tc.window || got.RemindersOnly != tc.reminders || got.Count != tc.count || got.Conflicting {
			t.Errorf("Dutch local task read lost filters: %q %+v %t", tc.input, got, ok)
		}
	}
	for _, input := range []string{"Toon geen taken", `"Toon mijn taken"`, "Maak een taak: koop melk", "Welke taken staan in het artikel?", "Wat is een taak?"} {
		if got, ok := TaskListRequest(input); ok {
			t.Errorf("non-collection became saved task read: %q %+v", input, got)
		}
	}
}
