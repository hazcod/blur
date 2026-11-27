package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

// This is a stateful, coverage-guided campaign, not a generated utterance list.
// Fuzz bytes choose actions and their order against real encrypted storage,
// Entry, native Ask, task/reminder services and deletion. The independent model
// retains live source IDs and task states; it never derives expectations from
// the planner. Go keeps and minimizes inputs that discover new paths or fail.
// A million executions is a stress budget, not a million distinct user needs.
func FuzzPersonalMemoryJourney(f *testing.F) {
	for action := byte(0); action < 14; action++ {
		f.Add([]byte{action, 0, 2, 0, 3, 0, 5, 0, 2, 0})
	}
	f.Add([]byte{6, 1, 7, 2, 8, 3, 9, 0, 4, 0, 5, 0, 10, 0, 11, 0})
	f.Add([]byte{7, 0, 7, 0, 7, 0, 7, 0, 7, 0, 7, 0, 7, 0, 7, 0, 7, 0})
	f.Add([]byte{13, 128, 5, 0, 2, 1, 4, 1, 11, 1, 13, 255})
	f.Add([]byte{13, 64, 13, 192, 5, 0, 2, 1, 13, 193})
	f.Fuzz(func(t *testing.T, input []byte) {
		// Bound work per journey without skipping mutated inputs. Every input
		// creates evidence and checks read-only task listing, even when empty.
		if len(input) > 32 {
			input = input[:32]
		}
		hash := sha256.Sum256(input)
		// Resolve the application's zone explicitly; the oracle's day boundary
		// is independent of the host process's cached time.Local location.
		zone, offset := "UTC", 0
		if hash[20]&1 != 0 {
			zone, offset = "Asia/Kathmandu", 20700
		}
		t.Setenv("TZ", zone)
		oracleZone := time.FixedZone("journey expected offset", offset)
		ctx := context.Background()
		path, key := filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey()
		now := time.Date(2026, 11, 21, 15, 30, 0, 0, time.UTC)
		var s *durable.Store
		var c *Client
		var reminderService *reminders.Service
		noticePort := &clientNoticePort{}
		closeMemory := func() {
			if c != nil {
				c.Close()
			}
			if reminderService != nil {
				reminderService.Close()
			}
			if s != nil {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
		}
		openMemory := func(mode durable.OpenMode) {
			var err error
			s, err = durable.OpenDurable(path, key, mode)
			if err != nil {
				t.Fatal(err)
			}
			deleter, err := deletion.NewService(s, nil)
			if err != nil {
				t.Fatal(err)
			}
			c = NewClient(s).WithNotes(notes.NewService(s, nil, func() time.Time { return now })).WithDeletion(deleter)
			c.askNow = func() time.Time { return now }
			reminderService, err = reminders.NewService(s, noticePort, reminders.Options{Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			c.reminders = reminderService
		}
		openMemory(durable.CreateNew)
		defer closeMemory()
		type source struct {
			id, token, text string
			live, self      bool
		}
		var sources []source
		var expectedTasks []tasks.Item
		var trace []string
		check := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatalf("journey %x trace=%v: %v", input, trace, err)
			}
		}
		addCapture := func(parameter byte) {
			apps := []string{"com.tinyspeck.slackmacgap", "com.apple.mail", "com.apple.Safari", "com.microsoft.teams2", "com.apple.Terminal", "com.apple.Preview", "com.microsoft.VSCode", "org.mozilla.firefox", "sh.blur.voice", "com.example.customcanvas", "com.hazcod.blur"}
			app := apps[int(parameter)%len(apps)]
			token := fmt.Sprintf("journey%xtopic%d", hash[:4], len(sources))
			facts := []string{
				"José confirmed the pentest retest for %s. The approved scope is stored in the security project folder.",
				"José recorded the deployment decision for %s. The release requires a successful restore rehearsal.",
				"José shared the train itinerary for %s. The booking reference is stored in the travel folder.",
				"José explained the invoice discrepancy for %s. The corrected total is in the finance worksheet.",
				"José saved the research reading notes for %s. The paper compares local language model inference costs.",
				"José confirmed the school pickup plan for %s. The agreed meeting point is the library entrance.",
				"José recorded the contractor visit for %s. The maintenance report is in the house documents folder.",
				"José confirmed the supplier renewal for %s. The approval record is stored in the procurement folder.",
			}
			text := fmt.Sprintf(facts[int(parameter)%len(facts)], token)
			// Vary match positions, including a long page tail, rather than
			// changing only question spelling. No screenshot/OCR is exercised.
			if parameter&16 != 0 {
				text = strings.Repeat("Unrelated navigation and document details. ", 1800) + text
			}
			o, err := capture.SanitizeObservation(app, "work.example", text, capture.MaxObservationTextBytes)
			check(err)
			o = o.WithTitle("Supplier renewal · José · " + token)
			check(s.PutObservationAt(ctx, o, now.Add(-time.Duration(parameter%30)*24*time.Hour)))
			sources = append(sources, source{o.ContentID(), token, o.Text(), true, app == "com.hazcod.blur"})
		}
		addCapture(hash[0])
		for step := 0; step+1 < len(input); step += 2 {
			action, parameter := input[step]%13, input[step+1]
			// Preserve the decoding of saved minimized failures. Reserve the
			// previously redundant selector 13 for event-time recall.
			if input[step] == 13 {
				action = 13
			}
			index := int(parameter) % len(sources)
			item := &sources[index]
			trace = append(trace, fmt.Sprintf("%d:%d/source%d", step/2, action, index))
			switch action {
			case 0:
				addCapture(parameter)
			case 1: // Repeated sightings must keep the exact authoritative source.
				if item.live && !item.self {
					o, found, err := s.GetObservation(ctx, item.id)
					check(err)
					if !found {
						t.Fatalf("live source missing: %v", trace)
					}
					before, _, _, err := s.ObservationSightingStats(ctx, item.id)
					check(err)
					check(s.RecordObservationSighting(ctx, o, now.Add(time.Duration(step+1)*time.Minute)))
					after, _, found, err := s.ObservationSightingStats(ctx, item.id)
					check(err)
					if !found || after <= before {
						t.Fatalf("repeat sighting lost: %d -> %d trace=%v", before, after, trace)
					}
				}
			case 2, 3: // Ask must cite live evidence and exclude erased/self text.
				for rounds := 0; ; rounds++ {
					more, err := s.IndexPendingExact(ctx, 32)
					check(err)
					if !more {
						break
					}
					if rounds > 100 {
						t.Fatal("index failed to drain")
					}
				}
				questions := []string{
					"What did I read about %s?", "What do I know about %s?",
					"What did we decide about %s?", "Find %s",
					"Where is the record for %s?", "What was mentioned about %s?",
				}
				question := fmt.Sprintf(questions[int(parameter)%len(questions)], item.token)
				result, err := c.AskMemory(ctx, question)
				check(err)
				for _, id := range result.Answer.SourceIDs {
					if !slices.ContainsFunc(sources, func(s source) bool { return s.id == id && s.live && !s.self }) {
						t.Fatalf("ungrounded citation %s trace=%v", id, trace)
					}
				}
				if item.live && !item.self && !slices.Contains(result.Answer.SourceIDs, item.id) {
					t.Fatalf("lost requested source %s: %+v trace=%v", item.token, result, trace)
				}
				if (!item.live || item.self) && (!result.Answer.InsufficientEvidence || len(result.Answer.SourceIDs) != 0) {
					t.Fatalf("unrelated evidence answered erased/self target: %+v trace=%v", result, trace)
				}
			case 4:
				if item.live {
					_, err := c.DeleteSource(item.id)
					check(err)
					item.live = false
				}
			case 5:
				closeMemory()
				openMemory(durable.OpenExisting)
			case 6: // A note containing TODO is saved as evidence, not an active task.
				token := fmt.Sprintf("journey%xtopic%d", hash[:4], len(sources))
				body := "TODO: Review " + token + " with Chloé.\nQuoted message: remind me tomorrow to approve the budget."
				added, err := c.AddNote(body)
				check(err)
				sources = append(sources, source{added.SourceID, token, body, true, false})
			case 7, 8:
				title := fmt.Sprintf("review supplier packet %x item %d", hash[:3], len(expectedTasks))
				question := "add a task: " + title
				if action == 8 {
					question = fmt.Sprintf("remind me in %d minutes to %s", 1+int(parameter), title)
				}
				entry, err := c.ResolveEntry(ctx, question, EntryState{}, "UTC")
				check(err)
				var reminder *tasks.Reminder
				var wantDue time.Time
				if action == 8 {
					if entry.Action.Kind != intent.Reminder || entry.Draft == nil || entry.Draft.Reminder == nil || entry.Draft.Missing != "" {
						t.Fatalf("reminder lost: %+v trace=%v", entry, trace)
					}
					reminder = entry.Draft.Reminder
					wantDue = now.Add(time.Duration(1+int(parameter)) * time.Minute)
					if !reminder.DueAt.Equal(wantDue) {
						t.Fatalf("reminder instant %v want %v trace=%v", reminder.DueAt, wantDue, trace)
					}
					if entry.Draft.Title != title {
						t.Fatalf("reminder title changed: %q want %q trace=%v", entry.Draft.Title, title, trace)
					}
				} else if entry.Action.Kind != intent.Task || entry.Action.Text != title {
					t.Fatalf("task creation lost: %+v trace=%v", entry, trace)
				}
				created, err := c.CreateTask(ctx, title, reminder)
				check(err)
				check(created.ScheduleError)
				if created.Item.Title != title || created.Item.State != tasks.Open {
					t.Fatalf("creation changed requested task: %+v trace=%v", created, trace)
				}
				if action == 8 {
					if created.Item.Reminder == nil || !created.Item.Reminder.DueAt.Equal(wantDue) || created.Item.Delivery.State != tasks.Scheduled {
						t.Fatalf("reminder not persisted/scheduled: %+v trace=%v", created, trace)
					}
					queued := false
					for _, notice := range noticePort.pending {
						queued = queued || notice.Title == title && notice.Schedule.DueAt.Equal(wantDue)
					}
					if !queued {
						t.Fatalf("notification payload missing trace=%v", trace)
					}
				}
				expectedTasks = append(expectedTasks, created.Item)
			case 9:
				if len(expectedTasks) > 0 {
					i := int(parameter) % len(expectedTasks)
					task := expectedTasks[i]
					task.State = tasks.Completed
					updated, err := c.UpdateTask(ctx, task)
					check(err)
					check(updated.ScheduleError)
					if updated.Item.State != tasks.Completed || updated.Item.Title != task.Title {
						t.Fatalf("completion ignored: %+v trace=%v", updated, trace)
					}
					expectedTasks[i] = updated.Item
				}
			case 10: // Unrelated content cannot establish an answer.
				result, err := c.AskMemory(ctx, "What did I read about absentjourneytopic?")
				check(err)
				if !result.Answer.InsufficientEvidence || len(result.Answer.SourceIDs) != 0 {
					t.Fatalf("missing evidence answered: %+v trace=%v", result, trace)
				}
			case 11: // A follow-up must reload current evidence, including deletion.
				plan, err := assistant.PlanQuestion("What did I read about "+item.token+"?", now, nil)
				check(err)
				result, err := c.AskMemoryWithHistory(ctx, "Make it shorter", []assistant.Turn{{Question: plan.Question, Plan: plan, SourceIDs: []string{item.id}}})
				check(err)
				if item.live && !item.self && !slices.Contains(result.Answer.SourceIDs, item.id) {
					t.Fatalf("follow-up lost live source: %+v trace=%v", result, trace)
				}
				if (!item.live || item.self) && (!result.Answer.InsufficientEvidence || len(result.Answer.SourceIDs) != 0) {
					t.Fatalf("follow-up restored forbidden source trace=%v", trace)
				}
			case 12:
				addCapture(parameter % 8)
				item := sources[len(sources)-1]
				chunk := enrich.Chunk{SourceID: item.id, ModelKey: enrich.ModelKey("journey-fixture"), End: len(item.text), Next: len(item.text), Complete: true, Proposals: []derive.Proposal{{Kind: knowledge.Person, Name: "José", Fact: item.text, SourceID: item.id}}}
				check(s.ApplyEnrichment(ctx, chunk))
				for {
					more, err := s.IndexPendingLibraryNames(ctx, 1)
					check(err)
					if !more {
						break
					}
				}
				entities, err := s.FindInferredKnowledge(ctx, "jose")
				check(err)
				if !slices.ContainsFunc(entities, func(e knowledge.Entity) bool {
					return slices.ContainsFunc(e.Facts, func(f knowledge.Fact) bool { return strings.Contains(f.Text, item.token) })
				}) {
					t.Fatalf("folded Library lost new source trace=%v", trace)
				}
			case 13: // Event dates differ independently from observation dates.
				dutch := parameter&64 != 0
				if parameter&128 != 0 {
					// Both sources share a date/family cue, but only one has the
					// numbered topic. Distinct per-source reference tokens keep
					// later generic/deletion oracles independent and unambiguous.
					number := 1 + int(parameter)%28
					month := now.AddDate(0, 1+int(hash[step%len(hash)])%3, 0)
					event := time.Date(month.Year(), month.Month(), number, 12, 0, 0, 0, time.UTC)
					observed := now.AddDate(0, 0, -1-int(hash[(step+1)%len(hash)])%30)
					clock := fmt.Sprintf("%02d:%02d", int(parameter)%24, int(hash[step%len(hash)])%60)
					family := fmt.Sprintf("journey%xfamily%d", hash[:4], len(sources))
					kind := [][2]string{{"route", "bus"}, {"invoice", "review"}, {"project", "demo"}, {"case", "appointment"}}[int(hash[(step+2)%len(hash)])%4]
					date := event.Format("Monday, 2 January 2006")
					if dutch {
						kind = [][2]string{{"route", "bus"}, {"factuur", "controle"}, {"project", "demo"}, {"zaak", "afspraak"}}[int(hash[(step+2)%len(hash)])%4]
						date = journeyDutchDate(event)
					}
					app := []string{"com.apple.mail", "com.microsoft.Outlook", "com.apple.Safari"}[int(parameter)%3]
					var ids []string
					for i := 0; i < 2; i++ {
						token := fmt.Sprintf("journey%xtopic%d", hash[:4], len(sources))
						statedTime := clock
						if i > 0 {
							statedTime = fmt.Sprintf("%02d:%02d", (int(parameter)+1)%24, int(hash[step%len(hash)])%60)
						}
						body := fmt.Sprintf("%s %d %s confirmation for %s on %s at %s Europe/Brussels. An end time was not provided. Source reference %s.", kind[0], number+i, kind[1], family, event.Format("Monday, 2 January 2006"), statedTime, token)
						if dutch {
							body = fmt.Sprintf("%s %d %s bevestiging voor %s op %s om %s Europe/Brussels. Er is geen eindtijd opgegeven. Bronreferentie %s.", kind[0], number+i, kind[1], family, date, statedTime, token)
						}
						o, err := capture.SanitizeObservation(app, "appointments.example", body, capture.MaxObservationTextBytes)
						check(err)
						check(s.PutObservationAt(ctx, o, observed.Add(time.Duration(i)*time.Minute)))
						sources = append(sources, source{o.ContentID(), token, o.Text(), true, false})
						ids = append(ids, o.ContentID())
					}
					for {
						more, err := s.IndexPendingExact(ctx, 32)
						check(err)
						if !more {
							break
						}
					}
					question := fmt.Sprintf("What time is my %s %d %s %s on %s?", kind[0], number, kind[1], family, event.Format("Monday, 2 January 2006"))
					if dutch {
						question = fmt.Sprintf("Hoe laat is mijn %s %d %s %s op %s?", kind[0], number, kind[1], family, date)
					}
					result, err := c.AskMemory(ctx, question)
					check(err)
					if len(result.Sources) == 0 || result.Sources[0].SourceID != ids[0] || result.Sources[0].TermsRelaxed || !result.Sources[0].CapturedAt.Equal(observed) || !slices.Contains(result.Answer.SourceIDs, ids[0]) {
						t.Fatalf("date token outranked the numbered topic: %+v trace=%v", result, trace)
					}
					for _, source := range result.Sources {
						if source.SourceID == ids[1] && !source.TermsRelaxed {
							t.Fatalf("wrong numbered topic called an exact match: %+v trace=%v", result, trace)
						}
					}
					if parameter%2 == 0 {
						page, err := c.SearchMemoryPageContext(ctx, question)
						check(err)
						if len(page.Hits) == 0 || page.Hits[0].SourceID != ids[0] || page.Hits[0].Relaxed {
							t.Fatalf("Search lost the numbered topic: %+v trace=%v", page, trace)
						}
						for _, hit := range page.Hits {
							if hit.SourceID == ids[1] && !hit.Relaxed {
								t.Fatalf("Search called the wrong numbered topic exact: %+v trace=%v", page, trace)
							}
						}
					} else {
						params, err := json.Marshal(mcpSearchParams{Query: question, Limit: 8})
						check(err)
						wire, err := c.HandleMCPRead(ctx, localapi.RPCSearchMemory, params)
						check(err)
						var page mcpSearchResponse
						check(json.Unmarshal(wire, &page))
						if len(page.Results) == 0 || page.Results[0].SourceID != ids[0] || page.Results[0].PartialMatch {
							t.Fatalf("MCP lost the numbered topic: %s trace=%v", wire, trace)
						}
						for _, hit := range page.Results {
							if hit.SourceID == ids[1] && !hit.PartialMatch {
								t.Fatalf("MCP called the wrong numbered topic exact: %s trace=%v", wire, trace)
							}
						}
					}
					break
				}
				token := fmt.Sprintf("journey%xtopic%d", hash[:4], len(sources))
				event := now.AddDate(0, 0, 1+int(parameter)%90)
				observed := now.AddDate(0, 0, -1-int(hash[(step+1)%len(hash)])%30)
				clock := fmt.Sprintf("%02d:%02d", int(parameter)%24, int(hash[step%len(hash)])%60)
				body := fmt.Sprintf("Appointment confirmation: your %s appointment is %s at %s Europe/Brussels. An end time was not provided.", token, event.Format("Monday, 2 January 2006"), clock)
				if dutch {
					observed = now.AddDate(0, 0, -1-int(hash[(step+1)%len(hash)])%6)
					body = fmt.Sprintf("Bevestiging afspraak %s op %s om %s Europe/Brussels. Er is geen eindtijd opgegeven.", token, journeyDutchDate(event), clock)
				}
				app := []string{"com.apple.mail", "com.microsoft.Outlook", "com.apple.Safari"}[int(parameter)%3]
				o, err := capture.SanitizeObservation(app, "appointments.example", body, capture.MaxObservationTextBytes)
				check(err)
				check(s.PutObservationAt(ctx, o, observed))
				sources = append(sources, source{o.ContentID(), token, o.Text(), true, false})
				for {
					more, err := s.IndexPendingExact(ctx, 32)
					check(err)
					if !more {
						break
					}
				}
				question := fmt.Sprintf("What time is my %s appointment on %s?", token, event.Weekday())
				missingEnd := "end time was not provided"
				if dutch {
					question = fmt.Sprintf("Hoe laat is mijn afspraak %s op %s?", token, journeyDutchWeekdays[event.Weekday()])
					missingEnd = "geen eindtijd"
				}
				result, err := c.AskMemory(ctx, question)
				check(err)
				if !slices.Contains(result.Answer.SourceIDs, o.ContentID()) || result.Answer.InsufficientEvidence || !result.Plan.Scope.IsZero() {
					t.Fatalf("event date hid older confirmation: %+v trace=%v", result, trace)
				}
				if !strings.Contains(result.Answer.Text, clock) || !strings.Contains(result.Answer.Text, missingEnd) {
					t.Fatalf("event time or missing-end uncertainty lost: %+v trace=%v", result, trace)
				}
				if !slices.ContainsFunc(result.Sources, func(source assistant.Evidence) bool {
					return source.SourceID == o.ContentID() && source.CapturedAt.Equal(observed)
				}) {
					t.Fatalf("event date replaced observation provenance: %+v trace=%v", result, trace)
				}
				if dutch {
					question = fmt.Sprintf("Wat heb ik op %s gelezen over %s?", journeyDutchWeekdays[observed.Weekday()], token)
					read, err := c.AskMemory(ctx, question)
					check(err)
					localObserved := observed.In(oracleZone)
					wantStart := time.Date(localObserved.Year(), localObserved.Month(), localObserved.Day(), 0, 0, 0, 0, oracleZone)
					if !slices.Contains(read.Answer.SourceIDs, o.ContentID()) || read.Plan.Scope.IsZero() || !read.Plan.Scope.Start.Equal(wantStart) || !read.Plan.Scope.End.Equal(wantStart.Add(24*time.Hour)) {
						t.Fatalf("Dutch observation query lost actual capture date: %+v trace=%v", read, trace)
					}
				}
			}
			// Check the independent state after every operation. Capture, read,
			// note and Ask actions have no authority to create/complete tasks.
			page, err := c.TaskPage(ctx, "", "")
			check(err)
			if len(page.Items) != len(expectedTasks) {
				t.Fatalf("unexpected task mutation: %d want %d trace=%v", len(page.Items), len(expectedTasks), trace)
			}
			for _, expected := range expectedTasks {
				if !slices.ContainsFunc(page.Items, func(got tasks.Item) bool {
					return got.ID == expected.ID && got.State == expected.State && got.Title == expected.Title && (expected.Reminder == nil && got.Reminder == nil || expected.Reminder != nil && got.Reminder != nil && got.Reminder.DueAt.Equal(expected.Reminder.DueAt))
				}) {
					t.Fatalf("task state lost: %s trace=%v", expected.ID, trace)
				}
			}
			openReminders := 0
			for _, task := range expectedTasks {
				if task.State == tasks.Open && task.Reminder != nil {
					openReminders++
				}
			}
			if len(noticePort.pending) != openReminders {
				t.Fatalf("notification queue differs from open reminders: %d want %d trace=%v", len(noticePort.pending), openReminders, trace)
			}
			for _, notice := range noticePort.pending {
				id, _, valid := reminders.NotificationTaskID(notice.ID)
				if !valid || !slices.ContainsFunc(expectedTasks, func(task tasks.Item) bool {
					return task.ID == id && task.State == tasks.Open && task.Reminder != nil && task.Title == notice.Title && task.Reminder.DueAt.Equal(notice.Schedule.DueAt)
				}) {
					t.Fatalf("notification outlived or changed its task: %+v trace=%v", notice, trace)
				}
			}
			listQuestion := "what's on my task list?"
			if hash[(step+1)%len(hash)]&1 != 0 {
				listQuestion = "Welke taken staan nog open?"
			}
			listed, err := c.ResolveEntry(ctx, listQuestion, EntryState{}, "UTC")
			check(err)
			if listed.Answer == nil {
				t.Fatalf("task list not answered locally trace=%v", trace)
			}
			openCount := 0
			for _, task := range expectedTasks {
				if task.State == tasks.Open {
					openCount++
				}
			}
			if len(listed.Answer.Targets) != min(8, openCount) || !strings.Contains(listed.Answer.Answer.Text, fmt.Sprintf("%d open task", openCount)) {
				t.Fatalf("task list count/targets lost: %+v trace=%v", listed.Answer, trace)
			}
			for _, target := range listed.Answer.Targets {
				if !slices.ContainsFunc(expectedTasks, func(task tasks.Item) bool {
					return task.ID == target.ID && task.State == tasks.Open && strings.Contains(listed.Answer.Answer.Text, task.Title)
				}) {
					t.Fatalf("task list displayed wrong task: %+v trace=%v", target, trace)
				}
			}
			if openCount > 8 && !strings.Contains(listed.Answer.Answer.Text, fmt.Sprintf("%d more are available", openCount-8)) {
				t.Fatalf("task list omitted coverage limit trace=%v", trace)
			}
		}
	})
}

// Literal test-owned calendar names keep the source/query oracle independent
// of the production grammar and date parsing.
var journeyDutchWeekdays = [...]string{"zondag", "maandag", "dinsdag", "woensdag", "donderdag", "vrijdag", "zaterdag"}

func journeyDutchDate(at time.Time) string {
	months := [...]string{"", "januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"}
	return fmt.Sprintf("%s %d %s %d", journeyDutchWeekdays[at.Weekday()], at.Day(), months[at.Month()], at.Year())
}
