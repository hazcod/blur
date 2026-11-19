package client

import (
	"strings"
	"testing"
	"time"
)

func TestSearchStatusLineCountsResultsSummariesAndClosestMatches(t *testing.T) {
	cases := []struct {
		hits, summaries int
		relaxed, more   bool
		want            string
	}{
		{0, 0, false, false, "0 results"},
		{1, 0, false, false, "1 result"},
		{12, 0, false, false, "12 results"},
		{3, 2, false, false, "3 results · 2 summaries"},
		{3, 1, false, false, "3 results · 1 summary"},
		{4, 0, true, false, "4 results · closest matches included"},
		{4, 2, true, false, "4 results · 2 summaries · closest matches included"},
		{50, 0, false, true, "50 results; more matches are available. Narrow the search to see them."},
	}
	for _, c := range cases {
		if got := searchStatusLine(c.hits, c.summaries, c.relaxed, c.more); got != c.want {
			t.Errorf("searchStatusLine(%+v) = %q, want %q", c, got, c.want)
		}
	}
}

func TestSummaryHeadingNamesTheLevelAndPeriodInLocalTime(t *testing.T) {
	brussels, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Skip(err)
	}
	old := time.Local
	time.Local = brussels
	defer func() { time.Local = old }()
	cases := []struct {
		hit  MemorySummaryHit
		want string
	}{
		{MemorySummaryHit{Level: "day", Period: "2026-11-15", Start: time.Date(2026, 11, 14, 23, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 15, 23, 0, 0, 0, time.UTC)}, "Day summary · 15 Nov"},
		{MemorySummaryHit{Level: "week", Period: "2026-W46", Start: time.Date(2026, 11, 8, 23, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 15, 23, 0, 0, 0, time.UTC)}, "Week summary · 9–15 Nov"},
		{MemorySummaryHit{Level: "week", Period: "2026-W49", Start: time.Date(2026, 11, 29, 23, 0, 0, 0, time.UTC), End: time.Date(2026, 12, 6, 23, 0, 0, 0, time.UTC)}, "Week summary · 30 Nov–6 Dec"},
		{MemorySummaryHit{Level: "hour", Period: "hour:20261115T13Z", Start: time.Date(2026, 11, 15, 13, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 15, 14, 0, 0, 0, time.UTC)}, "Hour summary · 15 Nov, 14:00"},
	}
	for _, c := range cases {
		if got := summaryHeading(c.hit); got != c.want {
			t.Errorf("summaryHeading(%s) = %q, want %q", c.hit.ID, got, c.want)
		}
	}
}

func TestHitCaptionIsAppTitleThenTimeFallingBackToDomain(t *testing.T) {
	brussels, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Skip(err)
	}
	old := time.Local
	time.Local = brussels
	defer func() { time.Local = old }()
	at := time.Date(2026, 11, 15, 13, 47, 0, 0, time.UTC)
	withTitle := SearchHit{AppID: "com.tinyspeck.slackmacgap", Domain: "app.slack.com", WindowTitle: "Dima Vermeulen", CapturedAt: at}
	if got := searchHitCaption(withTitle); got != "Slack · Dima Vermeulen · 15 Nov 2026, 14:47" {
		t.Errorf("caption with title = %q", got)
	}
	noTitle := SearchHit{AppID: "com.kagi.kagimacOS", Domain: "tiiny.ai", CapturedAt: at}
	if got := searchHitCaption(noTitle); !strings.HasSuffix(got, "· tiiny.ai · 15 Nov 2026, 14:47") {
		t.Errorf("caption without a title must show the domain: %q", got)
	}
	bare := SearchHit{AppID: "com.apple.Terminal", TimeUnknown: true}
	if got := searchHitCaption(bare); !strings.Contains(got, "Capture time unavailable") || strings.Contains(got, "· ·") {
		t.Errorf("caption = %q", got)
	}
}

func TestExcerptRunsSplitAroundMatchedTermsAndKeepEveryWord(t *testing.T) {
	excerpt := "…Heads up: the Orfit external retest is scheduled for Thursday, Dima will send the scope tomorrow…"
	runs := excerptRuns(excerpt, []string{"dima", "retest"})
	var rebuilt strings.Builder
	var emphasised []string
	for i, run := range runs {
		if i > 0 {
			rebuilt.WriteByte(' ')
		}
		rebuilt.WriteString(run.text)
		if run.match {
			emphasised = append(emphasised, strings.Trim(run.text, ",.…"))
		}
	}
	if rebuilt.String() != excerpt {
		t.Fatalf("runs lose or reorder words:\n got %q\nwant %q", rebuilt.String(), excerpt)
	}
	if strings.Join(emphasised, ",") != "retest,Dima" {
		t.Fatalf("emphasised = %v", emphasised)
	}
	for _, run := range runs {
		if !run.match && len(strings.Fields(run.text)) > searchRunWords {
			t.Fatalf("a plain run wraps in blocks of at most %d words: %q", searchRunWords, run.text)
		}
	}
	if got := excerptRuns("no relevant words here", []string{"zebra"}); len(got) == 0 || got[0].match {
		t.Fatalf("no emphasis without a match: %+v", got)
	}
	if excerptRuns("", []string{"dima"}) != nil {
		t.Fatal("an empty excerpt has no runs")
	}
}

func TestSearchPageRendersClosestMatchesDividerSummariesAndPreviews(t *testing.T) {
	brussels, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Skip(err)
	}
	old := time.Local
	time.Local = brussels
	defer func() { time.Local = old }()
	d := &desktop{page: "Ask", askQuestion: "dima retest", tokens: themePalette(false), searchComplete: true}
	at := time.Date(2026, 11, 15, 13, 47, 0, 0, time.UTC)
	long := strings.Repeat("filler words that are not the match. ", 30) + "The retest is on Thursday." + strings.Repeat(" more filler text follows.", 30)
	d.searchHits = []SearchHit{
		{SourceID: "a1", AppID: "com.tinyspeck.slackmacgap", WindowTitle: "Dima Vermeulen", CapturedAt: at, Text: long, Excerpt: "…The retest is on Thursday. more filler…", MatchedTerms: []string{"dima", "retest"}},
		{SourceID: "b2", AppID: "com.apple.Terminal", CapturedAt: at, Text: "dima notes", Excerpt: "dima notes", MatchedTerms: []string{"dima"}, Relaxed: true},
	}
	d.searchSummaries = []MemorySummaryHit{{ID: "chronicle:day:2026-11-15", Level: "day", Period: "2026-11-15", Start: time.Date(2026, 11, 14, 23, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 15, 23, 0, 0, 0, time.UTC), Excerpt: "- Sunday retest scoping with Dima.", SourceIDsSample: []string{"a1"}}}
	_, _, text := memoryWidgetTree(t, d)
	for _, want := range []string{"Matching sources", "Slack · Dima Vermeulen · 15 Nov 2026, 14:47", "retest", "Closest matches", "Day summary · 15 Nov", "Sunday"} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered search page lacks %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "Closest matches") != 1 || strings.Index(text, "Closest matches") < strings.Index(text, "Slack") {
		t.Errorf("the divider belongs once, between all-terms and relaxed rows:\n%s", text)
	}
	if strings.Contains(text, "filler words that are not the match. filler words") {
		t.Errorf("rows must show the excerpt, not the full capture:\n%s", text)
	}
}

func TestExcerptRunsClipUnbrokenTokensSoTheRowCannotStretch(t *testing.T) {
	token := "https://evidence.buildwise.example/packets/" + strings.Repeat("0123456789abcdef", 12)
	runs := excerptRuns("open the packet "+token+" now retest", []string{"retest"})
	for _, run := range runs {
		for _, word := range strings.Fields(run.text) {
			if n := len([]rune(word)); n > searchWordRunes+1 { // the clip adds one ellipsis rune
				t.Fatalf("a %d rune token survived in %q", n, run.text)
			}
		}
	}
	var joined []string
	for _, run := range runs {
		joined = append(joined, run.text)
	}
	if !strings.Contains(strings.Join(joined, " "), "retest") || !strings.Contains(strings.Join(joined, " "), "https://evidence") {
		t.Fatalf("clipping lost the surrounding words: %v", joined)
	}
}
