package client

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

// FuzzCalendarRecallLifecycle varies the period, source identity, app, facts,
// mutations and restarts at runtime. The expected live IDs come directly from
// independently inserted observations, never from the planner or an LLM.
func FuzzCalendarRecallLifecycle(f *testing.F) {
	f.Add([]byte{0, 0, 0, 1, 2, 3, 4})
	f.Add([]byte{1, 32, 11, 3, 0, 2, 1, 3, 4})
	f.Add([]byte{4, 1, 2, 2, 2, 3, 3, 4})
	f.Add([]byte{7, 33, 9, 0, 1, 3, 2, 0, 4})
	f.Add([]byte{0, 32, 0, 5, 6, 3, 4})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 16 {
			data = data[:16]
		}
		var parameters [3]byte
		copy(parameters[:], data)
		seed := sha256.Sum256(data)
		year, month := 1992+int(parameters[1]%34), time.Month(1+parameters[2]%12)
		type app struct{ id, name string }
		apps := [...]app{{"com.apple.mail", "Mail"}, {"com.tinyspeck.slackmacgap", "Slack"}, {"com.apple.Safari", "Safari"}, {"com.apple.Terminal", "Terminal"}}
		origin := apps[int(parameters[0]/2)%len(apps)]
		question := fmt.Sprintf("Summarize my activity in %s in %d", origin.name, year)
		first := time.Date(year, 1, 3+int(seed[0]%8), 10, 0, 0, 0, time.UTC)
		last := time.Date(year, 12, 20+int(seed[1]%8), 10, 0, 0, 0, time.UTC)
		if parameters[0]&1 != 0 {
			months := [...]string{"", "January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
			question = fmt.Sprintf("Summarize my activity in %s in %s %d", origin.name, months[month], year)
			first = time.Date(year, month, 2+int(seed[0]%4), 10, 0, 0, 0, time.UTC)
			last = time.Date(year, month, 24+int(seed[1]%4), 10, 0, 0, 0, time.UTC)
		}
		ctx, path, key := context.Background(), filepath.Join(t.TempDir(), "calendar.db"), securestore.OfflineFixtureKey()
		var store *durable.Store
		var client *Client
		open := func(mode durable.OpenMode) {
			var err error
			store, err = durable.OpenDurable(path, key, mode)
			if err != nil {
				t.Fatal(err)
			}
			client = NewClient(store)
			client.askNow = func() time.Time { return time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC) }
		}
		close := func() {
			client.Close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
		}
		open(durable.CreateNew)
		defer func() { close() }()
		var live [3]string
		var history []assistant.Turn
		var trace []byte
		put := func(slot, step int) {
			if live[slot] != "" {
				if err := store.DeleteObservationRecord(ctx, live[slot]); err != nil {
					t.Fatal(err)
				}
			}
			facts := [...]string{"supplier invoice review", "train booking evidence", "research notes", "release decision", "school arrangements", "house maintenance report", "renewal approval", "design feedback"}
			text := fmt.Sprintf("Reviewed %s, phase %d, reference calendar%xstep%d.", facts[int(seed[2+slot])%len(facts)], slot, seed[:4], step)
			observation, err := capture.SanitizeObservation(origin.id, "", text, capture.MaxObservationTextBytes)
			if err != nil {
				t.Fatal(err)
			}
			at := first
			if slot == 1 {
				at = last
			}
			if slot == 2 {
				at = time.Date(year, 2, 6+int(seed[4]%12), 10, 0, 0, 0, time.UTC)
				if parameters[0]&1 != 0 {
					at = time.Date(year, month, 14, 10, 0, 0, 0, time.UTC)
				}
			}
			if err := store.PutObservationAt(ctx, observation, at); err != nil {
				t.Fatal(err)
			}
			live[slot] = observation.ContentID()
		}
		put(0, -2)
		put(1, -1)
		// A matching period in another app cannot satisfy the requested scope.
		distractor, err := capture.SanitizeObservation("com.example.unrelated", "", "Reviewed an unrelated item in another app.", capture.MaxObservationTextBytes)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservationAt(ctx, distractor, first); err != nil {
			t.Fatal(err)
		}
		for step := 0; step <= len(data); step++ {
			if step < len(data) {
				op := data[step] % 5
				if data[step] == 5 || data[step] == 6 {
					op = data[step]
				}
				trace = append(trace, op)
				switch op {
				case 0, 1:
					put(int(op), step)
				case 2:
					slot := int(data[step]/5) % len(live)
					if live[slot] != "" {
						if err := store.DeleteObservationRecord(ctx, live[slot]); err != nil {
							t.Fatal(err)
						}
						live[slot] = ""
					}
				case 3:
					close()
					open(durable.OpenExisting)
				case 4:
					if len(history) != 0 {
						followup, err := client.AskMemoryWithHistory(ctx, "Make it shorter", history)
						if err != nil {
							t.Fatalf("follow-up %x trace=%v: %v", data, trace, err)
						}
						for _, id := range followup.Answer.SourceIDs {
							if !slices.Contains(live[:], id) {
								t.Fatalf("stale follow-up citation %x trace=%v: %s", data, trace, id)
							}
						}
					}
				case 5:
					put(2, step)
				case 6:
					for slot := 0; slot < 2; slot++ {
						if live[slot] != "" {
							if err := store.DeleteObservationRecord(ctx, live[slot]); err != nil {
								t.Fatal(err)
							}
							live[slot] = ""
						}
					}
				}
			}
			result, err := client.AskMemory(ctx, question)
			if err != nil {
				t.Fatalf("recall %x trace=%v question=%q: %v", data, trace, question, err)
			}
			for slot, id := range live {
				if id != "" && !slices.Contains(result.Answer.SourceIDs, id) && (slot < 2 || parameters[0]&1 != 0 || !result.CoverageLimited) {
					t.Fatalf("live boundary source missing %x trace=%v question=%q: %+v", data, trace, question, result)
				}
			}
			for _, id := range result.Answer.SourceIDs {
				if !slices.Contains(live[:], id) {
					t.Fatalf("unrelated/deleted citation %x trace=%v: %s", data, trace, id)
				}
			}
			if live[0] == "" && live[1] == "" && live[2] == "" && !result.Answer.InsufficientEvidence {
				t.Fatalf("empty scope answered %x trace=%v: %+v", data, trace, result)
			}
			if live[2] != "" && result.Answer.InsufficientEvidence && (!result.CoverageLimited || strings.Contains(result.Answer.Text, "Nothing captured") || strings.Contains(result.Answer.Text, "haven't captured anything")) {
				t.Fatalf("incomplete sample claimed no saved activity %x trace=%v: %+v", data, trace, result)
			}
			history = []assistant.Turn{{Question: question, Plan: result.Plan, Answer: result.Answer.Text, SourceIDs: result.Answer.SourceIDs}}
		}
	})
}
