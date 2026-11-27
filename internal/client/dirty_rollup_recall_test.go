package client

import (
	"context"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/chronicle"
)

func TestDirtyDayRollupUsesFreshHourEvidence(t *testing.T) {
	f := newAskFixture(t)
	defer f.client.Close()
	source := rollupFixtureSource(1)
	f.client.chronicles = rollupChronicles{
		hours:   []chronicle.Summary{hourRecapAt(10, 9, "The release approval was revoked.", source)},
		rollups: []chronicle.Rollup{rollupFixtureRecord(t, "day:2026-11-10", "The release was approved.", true, source)},
	}
	got, err := f.client.AskMemory(context.Background(), "Summarize yesterday")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Answer.Text, "approval was revoked") || strings.Contains(got.Answer.Text, "The release was approved.") {
		t.Fatalf("stale parent hid its current hour evidence: answer=%q sources=%+v", got.Answer.Text, got.Sources)
	}
	if layerCount(got.Sources, assistant.LayerHour) != 1 || layerCount(got.Sources, assistant.LayerDay) != 0 {
		t.Fatalf("dirty day remained authoritative: %+v", got.Sources)
	}
	if !got.CoverageLimited {
		t.Fatal("answer hid pending calendar recap freshness")
	}
}

func TestDirtyRollupWithoutFreshChildrenReportsPendingCoverage(t *testing.T) {
	for _, text := range []string{"The release was approved.", ""} {
		f := newAskFixture(t)
		f.client.chronicles = rollupChronicles{rollups: []chronicle.Rollup{
			rollupFixtureRecord(t, "day:2026-11-10", text, true, rollupFixtureSource(1)),
		}}
		got, err := f.client.AskMemory(context.Background(), "Summarize yesterday")
		f.client.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !got.CoverageLimited || !got.Answer.InsufficientEvidence || len(got.Sources) != 0 {
			t.Fatalf("pending stale/empty rollup was presented as current evidence: limited=%v answer=%+v sources=%+v", got.CoverageLimited, got.Answer, got.Sources)
		}
		if !strings.Contains(strings.ToLower(got.Answer.Text), "refresh") && !strings.Contains(strings.ToLower(got.Answer.Text), "pending") {
			t.Fatalf("empty answer hid pending recap work: %q", got.Answer.Text)
		}
	}
}

func TestDirtyWeekRollupUsesFreshDayEvidence(t *testing.T) {
	f := newAskFixture(t)
	defer f.client.Close()
	source := rollupFixtureSource(1)
	f.client.chronicles = rollupChronicles{rollups: []chronicle.Rollup{
		rollupFixtureRecord(t, "week:2026-W45", "The release was approved.", true, source),
		rollupFixtureRecord(t, "day:2026-11-02", "The release approval was revoked.", false, source),
	}}
	got, err := f.client.AskMemory(context.Background(), "Summarize last week")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Answer.Text, "approval was revoked") || strings.Contains(got.Answer.Text, "The release was approved.") || layerCount(got.Sources, assistant.LayerWeek) != 0 || layerCount(got.Sources, assistant.LayerDay) != 1 {
		t.Fatalf("stale week replaced its current day evidence: answer=%q sources=%+v", got.Answer.Text, got.Sources)
	}
}
