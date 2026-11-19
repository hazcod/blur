package client

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/platform/darwin"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

// scenarioCorpus is a realistic, labelled personal-memory fixture. Every
// capture is written through the durable store with an explicit capture time,
// the activity engine records the same sightings production ingest records,
// and notes, voice transcripts and hourly recaps use their production paths.
// The planning clock is 2026-11-16 15:30 Europe/Brussels.
type scenarioCorpus struct {
	client *Client
	store  *durable.Store
	loc    *time.Location
	now    time.Time

	ids        map[string]string    // label -> source ID
	labels     map[string]string    // source ID -> label
	times      map[string]time.Time // label -> first capture time
	hourText   map[string]string    // recap text -> hour ID
	hourLabels map[string][]string  // hour ID -> labels of supporting sources
}

func (c *scenarioCorpus) label(sourceID string) string {
	if label, ok := c.labels[sourceID]; ok {
		return label
	}
	return ""
}

// citedLabels maps evidence back to corpus labels. A recap (hour) evidence item
// is expanded to the labels of the captures it summarises.
func (c *scenarioCorpus) evidenceLabels(sourceID, text string, hour bool) []string {
	if label := c.label(sourceID); label != "" {
		return []string{label}
	}
	if hour {
		if id, ok := c.hourText[text]; ok {
			return append([]string(nil), c.hourLabels[id]...)
		}
	}
	if len(sourceID) > 12 {
		sourceID = sourceID[:12]
	}
	return []string{"unknown:" + sourceID}
}

type scenarioCapture struct {
	label, app, domain string
	// title is the window title the capture was recorded with, seeded through
	// the title-aware path (capture.SanitizedObservation.WithTitle).
	title     string
	at        string // local Europe/Brussels "2006-01-02 15:04"
	text      string
	sightings []time.Duration // later visits; default {2m}
}

const scenarioTimeLayout = "2006-01-02 15:04"

func scenarioLocation(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Skipf("Europe/Brussels zone data unavailable: %v", err)
	}
	return loc
}

func slackScreen(channel string, sidebar []string, lines ...string) string {
	var b strings.Builder
	b.WriteString("Slack\nFile Edit View Go Window Help\nCresco Security\nHome\nDMs\nActivity\nFiles\nMore\n")
	b.WriteString("Direct messages\n")
	for _, name := range sidebar {
		b.WriteString(name + "\n")
	}
	b.WriteString("Channels\n# general\n# security-team\n# random\n")
	b.WriteString(channel + "\n")
	for _, line := range lines {
		b.WriteString(line + "\n")
	}
	b.WriteString("Message " + channel + "\nAa  @  Send\n")
	return b.String()
}

func scenarioOrionArticle() string {
	var b strings.Builder
	b.WriteString("Tiiny Pocket runs a 120B model without a cloud - Tiiny AI blog\n")
	b.WriteString("Tiiny Pocket runs a 120B model without a cloud, and this article explains how local LLMs got there.\n")
	topics := []string{"quantization", "memory bandwidth", "mixture of experts", "speculative decoding", "context caching", "thermal limits", "NPU offload", "flash storage streaming"}
	for i := 0; b.Len() < 64800; i++ {
		topic := topics[i%len(topics)]
		fmt.Fprintf(&b, "Section %d. Running local LLMs depends on %s. Researchers measured the effect of %s on tokens per second across %d devices and found a %d percent change in latency when the model weights stayed resident in fast memory. Teams that run models on their own hardware keep prompts on the device, avoid per request fees and can work offline, although larger models still need careful tuning of batch size and cache layout.\n", i+1, topic, topic, 3+i%9, 7+i%23)
	}
	b.WriteString("Conclusion. Local LLMs are now practical for private work, and the last paragraph mentions the always-on companion that keeps a small model warm at home.\n")
	return b.String()
}

func scenarioTerminalVenice() string {
	var b strings.Builder
	b.WriteString("niels@mbp ~/Source/infra % ")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "ls -la releases/2026-11-%02d\ntotal %d\ndrwxr-xr-x  4 niels  staff  128 Nov 12 15:%02d build-%d\n", 1+i%9, 8+i, i%60, 100+i)
	}
	b.WriteString("export VENICE_API_KEY=$(op read \"op://Infra/venice/api-key\")\n")
	for i := 0; i < 19; i++ {
		fmt.Fprintf(&b, "git log --oneline -n %d\n%07x fix flaky test %d\n", 1+i%4, 0xabc1230+i, i)
	}
	return b.String()
}

func scenarioCaptures() []scenarioCapture {
	dmSidebar := []string{"Dima Vermeulen", "Samira Peeters", "Chloe Britton", "Youssef Bey"}
	return []scenarioCapture{
		{label: "slack-dima-1", title: "Dima Vermeulen (DM) - Cresco - Slack", app: "com.tinyspeck.slackmacgap", domain: "app.slack.com", at: "2026-11-14 10:12",
			text: slackScreen("Dima Vermeulen", dmSidebar,
				"Dima Vermeulen  10:08", "Morning! Did Cresco IT approve the pentest access request yet?",
				"Niels Hofmans  10:10", "Yes, the VPN profile is in. I will start recon on the portal today.",
				"Dima Vermeulen  10:11", "Great, ping me when the first findings draft is ready.")},
		{label: "slack-dima-2", title: "Dima Vermeulen (DM) - Cresco - Slack", app: "com.tinyspeck.slackmacgap", domain: "app.slack.com", at: "2026-11-14 16:40",
			text: slackScreen("Dima Vermeulen", dmSidebar,
				"Niels Hofmans  16:31", "Draft findings are in the shared folder: 3 high and 5 medium issues.",
				"Dima Vermeulen  16:36", "Thanks, I will go through them over the weekend.",
				"Dima Vermeulen  16:37", "No rush, the client wants the final report end of next week.")},
		{label: "slack-dima-retest", title: "Dima Vermeulen (DM) - Cresco - Slack", app: "com.tinyspeck.slackmacgap", domain: "app.slack.com", at: "2026-11-15 14:47",
			text: slackScreen("Dima Vermeulen", dmSidebar,
				"Dima Vermeulen  14:41", "Hi Niels, the client fixed the two critical issues from the report.",
				"Dima Vermeulen  14:42", "Heads up: the Orfit external retest is scheduled for Thursday 20 November, I will send the scope tomorrow",
				"Niels Hofmans  14:45", "Perfect, I will block Thursday in my calendar.")},
		{label: "slack-samira-1", title: "Samira Peeters (DM) - Cresco - Slack", app: "com.tinyspeck.slackmacgap", domain: "app.slack.com", at: "2026-11-13 11:05",
			text: slackScreen("Samira Peeters", dmSidebar,
				"Samira Peeters  11:00", "Hey Niels, can you help me with the AG presentations?",
				"Samira Peeters  11:01", "The deadline is end of day Friday, the client wants the deck for the AG meeting.",
				"Niels Hofmans  11:03", "Sure, send me the outline and I will draft the security slides.")},
		{label: "slack-samira-2", title: "Samira Peeters (DM) - Cresco - Slack", app: "com.tinyspeck.slackmacgap", domain: "app.slack.com", at: "2026-11-14 09:20",
			text: slackScreen("Samira Peeters", dmSidebar,
				"Samira Peeters  09:15", "I started the AG presentations deck, still missing the threat model slide.",
				"Niels Hofmans  09:18", "I will send my pentest summary slide this afternoon.")},
		{label: "slack-samira-3", title: "Samira Peeters (DM) - Cresco - Slack", app: "com.tinyspeck.slackmacgap", domain: "app.slack.com", at: "2026-11-15 10:30",
			text: slackScreen("Samira Peeters", dmSidebar,
				"Samira Peeters  10:26", "Reminder: the AG presentations are due end of day Friday.",
				"Niels Hofmans  10:29", "On it, I am sending the draft deck tonight.")},
		{label: "slack-samira-4", title: "Samira Peeters (DM) - Cresco - Slack", app: "com.tinyspeck.slackmacgap", domain: "app.slack.com", at: "2026-11-15 17:05",
			text: slackScreen("Samira Peeters", dmSidebar,
				"Samira Peeters  17:01", "Got the deck, it looks good. Can you shorten the agenda slide?",
				"Samira Peeters  17:02", "The AG presentations review with the client is on Monday.")},
		{label: "mail-inbox", title: "Inbox - Cresco (12 messages)", app: "com.apple.mail", at: "2026-11-14 08:50",
			text: "Mail\nFile Edit View Mailbox Message Format Window Help\nMailboxes\nInbox 12\nFlagged\nDrafts\nSent\nArchive\nInbox - Cresco\nBart Janssens  Invoice October  08:12  Please find the invoice for October attached.\nChloe Britton  Consultant reporting  08:31  Hi Niels, can you send me the consultant reporting template by Wednesday?\nCresco IT  VPN certificate renewal  07:58  Your VPN certificate expires in 14 days.\nOkta  New sign-in to your account  07:40  We noticed a new sign-in from Brussels.\n"},
		{label: "mail-chloe", title: "Consultant reporting", app: "com.apple.mail", at: "2026-11-14 08:55",
			text: "Mail\nFile Edit View Mailbox Message Format Window Help\nInbox - Cresco\nFrom: Chloe Britton <chloe.britton@cresco.example>\nTo: Niels Hofmans\nSubject: Consultant reporting\nDate: 14 November 2026 at 08:31\nHi Niels,\nCan you send me the consultant reporting template by Wednesday? I need it to consolidate the hours from the pentest team.\nThanks,\nChloe\nReply  Reply All  Forward  Archive  Trash\n"},
		{label: "orion-article", title: "Tiiny Pocket | Personal AI Infrastructure for Local LLMs", app: "com.kagi.kagimacOS", domain: "tiiny.ai", at: "2026-11-16 10:07", sightings: []time.Duration{7 * time.Minute},
			text: scenarioOrionArticle()},
		{label: "terminal-venice", title: "niels@mbp: ~/Source/infra — zsh", app: "com.apple.Terminal", at: "2026-11-12 15:22", text: scenarioTerminalVenice()},
		{label: "notion-pentest-plan", title: "Pentest planning Q4", app: "notion.id", domain: "notion.so", at: "2026-11-16 15:05",
			text: "Pentest planning Q4\nNotion\nShare  Updates  Favorite\nOwner: Niels Hofmans\nGoals\n- External retest for Orfit\n- Idelux local project report\n- Buildwise evidence packet\nMilestones\n- Scope agreed with client\n- Testing window booked\n- Report review with Dima\n"},
		{label: "okta-login", title: "Sign in to Cresco", app: "com.kagi.kagimacOS", domain: "cresco.okta.com", at: "2026-11-15 09:00",
			text: "Cresco Okta\nSign in to Cresco — Dima pentest portal — Username — Password — Keep me signed in\nSign in\nNeed help signing in?\n"},
		{label: "blur-self", title: "Ask Blur", app: appidentity.BundleID, at: "2026-11-16 15:20",
			text: "Blur\nAsk Blur\nAsk Blur: what did Dima say about the pentest retest\nSearching your saved memory...\n"},
		{label: "teams-idelux", title: "Idelux project channel | Microsoft Teams", app: "com.microsoft.teams2", at: "2026-11-15 14:10",
			text: "Microsoft Teams\nChat  Teams  Calendar  Calls\nIdelux project channel\nIdelux local project — need to report, testing period 20–21 October\nReply  Reactions\nFiles  Wiki  Planner\n"},
		{label: "preview-programme", title: "security-first-2026-programme.pdf", app: "com.apple.Preview", at: "2026-11-15 16:30",
			text: "Preview\nFile Edit View Go Tools Window Help\nsecurity-first-2026-programme.pdf\nSecurity First 2026 Brussels programme\nYoussef Bey project lead\nPage 1 of 6\nKeynote 09:30  Workshops 11:00  Closing 16:00\n"},
		{label: "finder-downloads", title: "Downloads", app: "com.apple.finder", at: "2026-11-15 09:30",
			text: "Finder\nFile Edit View Go Window Help\nFavorites\nAirDrop\nRecents\nApplications\nDownloads\nDownloads — 14 items\nName  Date Modified  Size\nreport-draft.pdf  Yesterday  1.2 MB\nprogramme.pdf  Yesterday  800 KB\n"},
		{label: "sublime-sync-1", title: "segments.go — blur sync segments", app: "com.sublimetext.4", at: "2026-11-09 14:00",
			text: "Sublime Text\nFile Edit Selection Find View Goto Tools Project Window Help\nsegments.go - blur sync segments\npackage sync\n// sealed segment folder sync: seal, upload, verify manifest\nfunc SealSegment(dir string) error {\n\treturn nil\n}\n"},
		{label: "terminal-sync-2", title: "niels@mbp: ~/Source/blur — zsh", app: "com.apple.Terminal", at: "2026-11-10 16:00",
			text: "niels@mbp ~/Source/blur % go test ./internal/sync -run Segments -count=1\nblur sync segments: sealed 12 segments, manifest verified\nok  \tgithub.com/hazcod/blur/internal/sync\t0.842s\nniels@mbp ~/Source/blur % \n"},
		// Folding scenario: the title and body spell "résumé" with accents, a
		// query for "resume" must still find it.
		{label: "preview-resume", title: "CV_Youssef_résumé.pdf", app: "com.apple.Preview", at: "2026-11-14 11:20",
			text: "Preview\nFile Edit View Go Tools Window Help\nCV_Youssef_résumé.pdf\nYoussef Bey\nSecurity consultant, Brussels\nrésumé\nExperience: penetration testing, red team engagements, client reporting\nPage 1 of 2\n"},
	}
}

var scenarioRecaps = map[string]string{
	"slack-dima-retest": "Chatted with Dima on Slack about the pentest retest",
	"teams-idelux":      "Checked the Idelux local project channel in Teams",
	"preview-programme": "Read the Security First 2026 Brussels programme",
	"slack-samira-4":    "Reviewed the AG presentations deck with Samira",
	"orion-article":     "Read an article about local LLMs in Orion",
	"voice-youssef":     "Recorded a voice note about the call with Youssef",
}

// newScenarioCorpus builds the corpus once per test.
func newScenarioCorpus(t *testing.T) *scenarioCorpus {
	t.Helper()
	ctx := context.Background()
	loc := scenarioLocation(t)
	t.Setenv("TZ", "Europe/Brussels")
	oldLocal := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = oldLocal })

	corpus := &scenarioCorpus{
		loc: loc, now: time.Date(2026, 11, 16, 15, 30, 0, 0, loc),
		ids: map[string]string{}, labels: map[string]string{}, times: map[string]time.Time{},
		hourText: map[string]string{}, hourLabels: map[string][]string{},
	}
	path, key := filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey()
	store, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = store.Close()
		}
	})

	parse := func(value string) time.Time {
		at, err := time.ParseInLocation(scenarioTimeLayout, value, loc)
		if err != nil {
			t.Fatal(err)
		}
		return at
	}

	type event struct {
		at       time.Time
		kind     activity.EventKind
		sourceID string
		app      string
		domain   string
	}
	var events []event
	visits := map[string][]time.Time{}
	for _, seed := range scenarioCaptures() {
		observation, err := capture.SanitizeObservation(seed.app, seed.domain, seed.text, capture.MaxObservationTextBytes)
		if err != nil {
			t.Fatalf("%s: %v", seed.label, err)
		}
		if seed.title == "" {
			t.Fatalf("%s: every corpus capture needs a window title", seed.label)
		}
		if observation = observation.WithTitle(seed.title); observation.Title() != seed.title {
			t.Fatalf("%s: the sanitizer changed the fixture title %q to %q", seed.label, seed.title, observation.Title())
		}
		if observation.Truncated() || observation.Text() != seed.text {
			t.Fatalf("%s: sanitizer changed the fixture text (redacted or truncated)", seed.label)
		}
		at := parse(seed.at)
		if err := store.PutObservationAt(ctx, observation, at.UTC()); err != nil {
			t.Fatalf("%s: %v", seed.label, err)
		}
		corpus.ids[seed.label], corpus.labels[observation.ContentID()], corpus.times[seed.label] = observation.ContentID(), seed.label, at
		sightings := seed.sightings
		if sightings == nil {
			sightings = []time.Duration{2 * time.Minute}
		}
		events = append(events, event{at: at, kind: activity.Seen, sourceID: observation.ContentID(), app: seed.app, domain: seed.domain})
		visits[seed.label] = append(visits[seed.label], at)
		last := at
		for _, offset := range sightings {
			seen := at.Add(offset)
			if err := store.RecordObservationSighting(ctx, observation, seen.UTC()); err != nil {
				t.Fatalf("%s sighting: %v", seed.label, err)
			}
			events = append(events, event{at: seen, kind: activity.Seen, sourceID: observation.ContentID(), app: seed.app, domain: seed.domain})
			visits[seed.label] = append(visits[seed.label], seen)
			last = seen
		}
		events = append(events, event{at: last.Add(time.Minute), kind: activity.Idle})
	}

	// Production ingest feeds one monotonic engine and persists each emitted
	// segment; replay the same events in order.
	sort.SliceStable(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
	engine := activity.New(activity.Config{})
	epoch := events[0].at
	for _, e := range events {
		segments, err := engine.Apply(activity.Event{Kind: e.kind, Monotonic: int64(e.at.Sub(epoch)), Wall: e.at.UTC(), SourceID: e.sourceID, AppID: e.app, Domain: e.domain})
		if err != nil {
			t.Fatalf("activity event rejected: %v", err)
		}
		for _, segment := range segments {
			if err := store.PutActivitySegment(ctx, segment); err != nil {
				t.Fatalf("activity segment rejected: %v", err)
			}
		}
	}
	scenarioIndex(t, store)

	// Reopen the encrypted store the way the desktop does before asking.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	store, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	corpus.store = store
	t.Cleanup(func() { _ = store.Close() })

	c := NewClient(store)
	c.askNow = func() time.Time { return corpus.now }
	t.Cleanup(c.Close)
	corpus.client = c

	// Typed note through the production notes path.
	noteAt := parse("2026-11-13 12:00")
	c.WithNotes(notes.NewService(store, nil, func() time.Time { return noteAt }))
	note, err := c.AddNote("Buildwise evidence packet URL: https://evidence.buildwise.example/packets/2026-11")
	if err != nil {
		t.Fatalf("note-buildwise: %v", err)
	}
	corpus.ids["note-buildwise"], corpus.labels[note.SourceID], corpus.times["note-buildwise"] = note.SourceID, "note-buildwise", noteAt

	// Voice note through the same recorded-content path as the voice tests.
	voiceAt := parse("2026-11-16 11:40")
	audioStream := &controllerAudioStream{batches: make(chan []darwin.MeetingAudioEvent, 64), controls: make(chan controllerAudioControl, 32), closed: make(chan struct{})}
	audioStream.autoComplete.Store(true)
	audio := &controllerAudioFixture{stream: audioStream}
	c.meetings.open = func() (meetingAudio, error) { return audio, nil }
	c.meetings.now = func() time.Time { return voiceAt }
	if err := c.StartMeeting(ctx, "Voice notes", "en-US", []string{"microphone"}); err != nil {
		t.Fatalf("voice-youssef start: %v", err)
	}
	meetingID := c.MeetingLive().Meeting.ID
	audioStream.emit(controllerState(meetingID, 1, "starting"), controllerState(meetingID, 1, "running"),
		controllerWords(meetingID, 1, "voice-final", "Call with Youssef: he will send the SOW by Friday and wants the AG deck first", true))
	waitController(t, c, func(v MeetingLiveView) bool { return len(v.Tail) == 1 })
	if err := c.ControlMeeting(ctx, "stop"); err != nil {
		t.Fatal(err)
	}
	waitController(t, c, func(v MeetingLiveView) bool { return v.State == "completed" })
	transcript, err := c.MeetingTranscript(ctx, meetingID, 0)
	if err != nil || len(transcript.Records) != 1 {
		t.Fatalf("voice-youssef was not saved: %v", err)
	}
	corpus.ids["voice-youssef"], corpus.labels[transcript.Records[0].SourceID], corpus.times["voice-youssef"] = transcript.Records[0].SourceID, "voice-youssef", voiceAt
	visits["voice-youssef"] = []time.Time{voiceAt}

	// Hourly recaps through the chronicle store, one per closed hour that has
	// at least one visited source: 2026-11-15 13:00-17:00, 2026-11-16 09:00-15:00.
	recapHours := []string{}
	for _, hour := range []string{"13", "14", "15", "16"} {
		recapHours = append(recapHours, "2026-11-15 "+hour+":00")
	}
	for _, hour := range []string{"09", "10", "11", "12", "13", "14"} {
		recapHours = append(recapHours, "2026-11-16 "+hour+":00")
	}
	for _, value := range recapHours {
		start := parse(value).UTC().Truncate(time.Hour)
		end := start.Add(time.Hour)
		var labels []string
		for label, times := range visits {
			if scenarioRecaps[label] == "" {
				continue
			}
			for _, visit := range times {
				if !visit.Before(start) && visit.Before(end) {
					labels = append(labels, label)
					break
				}
			}
		}
		if len(labels) == 0 {
			continue
		}
		sort.Strings(labels)
		var ids, bullets []string
		for _, label := range labels {
			ids = append(ids, corpus.ids[label])
			bullets = append(bullets, "- "+scenarioRecaps[label])
		}
		if len(bullets) < 2 {
			bullets = append(bullets, "- No other saved activity in this hour")
		}
		slices.Sort(ids)
		summary := chronicle.Summary{HourID: "hour:" + start.Format("20060102T15Z"), Start: start, End: end, Text: strings.Join(bullets, "\n"), SourceIDs: ids}
		if err := store.PutChronicle(ctx, summary); err != nil {
			t.Fatalf("recap %s: %v", summary.HourID, err)
		}
		corpus.hourText[summary.Text], corpus.hourLabels[summary.HourID] = summary.HourID, labels
		corpus.hourText[summary.Text] = summary.HourID
	}
	scenarioIndex(t, store)

	service, err := reminders.NewService(store, &clientNoticePort{}, reminders.Options{Now: func() time.Time { return corpus.now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close() })
	c.reminders = service
	return corpus
}

func scenarioIndex(t *testing.T, store *durable.Store) {
	t.Helper()
	for range 128 {
		more, err := store.IndexPendingExact(context.Background(), 100)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			return
		}
	}
	t.Fatal("exact index did not drain")
}
