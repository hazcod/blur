package assistant

import (
	"slices"
	"testing"
	"time"
)

func TestDutchRecallRetainsTopicsAndSeparatesEventFromObservationDates(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		question    string
		terms       []string
		event       bool
		observedDay int
		app         string
	}{
		{"Hoe laat is mijn afspraak fysiotherapie op maandag?", []string{"afspraak", "fysiotherapie", "maandag"}, true, 0, ""},
		{"Wanneer is mijn afspraak fysiotherapie op maandag?", []string{"afspraak", "fysiotherapie", "maandag"}, true, 0, ""},
		{"Hoe laat is mijn sessie over gelezen boeken op maandag?", []string{"boeken", "maandag", "sessie"}, true, 0, ""},
		{"Wat heb ik donderdag gelezen over fysiotherapie in Mail?", []string{"fysiotherapie"}, false, 19, "com.apple.mail"},
		{"Wanneer is mijn afspraak fysiotherapie op maandag uit de mail die ik donderdag heb gelezen?", []string{"afspraak", "fysiotherapie", "maandag"}, true, 19, ""},
		{"Zoek mijn factuur 23 in Mail", []string{"23", "factuur"}, false, 0, "com.apple.mail"},
		{"Vind mijn café notities van gisteren", []string{"cafe", "notities"}, false, 20, ""},
	} {
		for _, planner := range []func(string, time.Time, []KnownApp) (Plan, error){PlanQuestion, PlanSearch} {
			p, err := planner(tc.question, now, nil)
			if err != nil || p.Kind != KindLookup || !p.MatchOnly || p.EventTime != tc.event || p.Question != tc.question || !slices.Equal(p.Terms, tc.terms) {
				t.Errorf("Dutch request lost grammar/topic: %q %+v %v", tc.question, p, err)
			}
			if tc.observedDay == 0 && !p.Scope.IsZero() || tc.observedDay > 0 && (!p.Scope.Start.Equal(time.Date(2026, 11, tc.observedDay, 0, 0, 0, 0, time.UTC)) || !p.Scope.End.Equal(time.Date(2026, 11, tc.observedDay+1, 0, 0, 0, 0, time.UTC))) {
				t.Errorf("Dutch event/observation date confused: %q %+v", tc.question, p.Scope)
			}
			if tc.app != "" && !slices.Equal(p.AppIDs, []string{tc.app}) {
				t.Errorf("Dutch app filter lost: %+v", p)
			}
		}
	}
}

func TestDutchEventFollowupReplacesOnlyTheDate(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	p, err := PlanQuestion("Hoe laat is mijn route 23 bus op maandag 23 november 2026 in Mail?", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		question string
		terms    []string
	}{
		{"En op dinsdag 24 november 2026?", []string{"2026", "23", "24", "bus", "dinsdag", "november", "route"}},
		{"Woensdag?", []string{"23", "bus", "route", "woensdag"}},
	} {
		next, err := PlanQuestion(tc.question, now, nil)
		if err != nil {
			t.Fatal(err)
		}
		next = next.Inherit(p)
		want := slices.Clone(tc.terms)
		slices.Sort(want)
		if !IsFollowUp(tc.question) || !next.EventTime || !next.Scope.IsZero() || !slices.Equal(next.Terms, want) || !slices.Contains(next.AppIDs, "com.apple.mail") || !slices.Equal(next.NumberedSubjects, [][2]string{{"route", "23"}}) {
			t.Fatalf("Dutch continuation changed topic/date: %q %+v", tc.question, next)
		}
		p = next
	}
}

func TestDutchGrammarDoesNotRewriteLiteralTopicsOrGeneralRequests(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	for _, question := range []string{"Wat is fotosynthese?", "Zoek op internet naar fysiotherapie", "Zoek op het internet naar fysiotherapie", "Zoek online naar fysiotherapie", "Zoek een manier om sneller te typen"} {
		p, err := PlanQuestion(question, now, nil)
		if err != nil || p.Kind != KindGeneral {
			t.Errorf("general request became personal recall: %q %+v %v", question, p, err)
		}
	}
	p, err := PlanSearch(`Zoek "Mijn Morgen" in Mail`, now, nil)
	if err != nil || !slices.Contains(p.Terms, "mijn") || !slices.Contains(p.Terms, "morgen") || slices.Contains(p.Terms, "zoek") || !p.Scope.IsZero() {
		t.Fatalf("Dutch grammar consumed a literal topic: %+v %v", p, err)
	}
	for _, question := range []string{"Zoek gisteren.log", "Zoek maandag.md in Mail", "Zoek fysiotherapie in maandag.example"} {
		p, err := PlanSearch(question, now, nil)
		if err != nil || !p.Scope.IsZero() {
			t.Errorf("Dutch file topic became a capture date: %q %+v %v", question, p, err)
		}
	}
}
