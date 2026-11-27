package meetings_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

type uncertainMeetingStore struct {
	*durable.Store
	failKey string
	failed  bool
}

func (s *uncertainMeetingStore) Append(ctx context.Context, record meetings.Record) (meetings.Receipt, error) {
	receipt, err := s.Store.Append(ctx, record)
	if err == nil && record.Key == s.failKey && !s.failed {
		s.failed = true
		return meetings.Receipt{}, errors.New("uncertain private adapter error")
	}
	return receipt, err
}

func TestEncryptedContinuationRetainsExactRetryAcrossEvictionAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.db")
	key := securestore.OfflineFixtureKey()
	store, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	metadata := meetings.Meeting{ID: "long-recording", Title: "Long recording", Locale: "en-US", StartedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC), State: meetings.Running, Tracks: []string{"microphone"}, Revision: 1}
	if err := store.PutMeeting(ctx, metadata, 0); err != nil {
		t.Fatal(err)
	}
	repo := &uncertainMeetingStore{Store: store, failKey: metadata.ID + "/transcript/microphone/segment-063"}
	session, err := meetings.New(ctx, meetings.Config{SessionID: metadata.ID, Tracks: metadata.Tracks}, repo)
	if err != nil {
		t.Fatal(err)
	}
	token, err := session.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var first, last meetings.Event
	for i := 0; i < 64; i++ {
		e := meetings.Event{Kind: meetings.Final, SessionID: metadata.ID, TrackID: "microphone", SegmentID: fmt.Sprintf("segment-%03d", i), Revision: 1, Start: 40*time.Hour + time.Duration(i)*time.Second, End: 40*time.Hour + time.Duration(i+1)*time.Second, Text: fmt.Sprintf("Exact recording evidence %03d", i)}
		if i == 0 {
			first = e
		}
		last = e
		err := session.Submit(ctx, token, e)
		if i < 63 && err != nil {
			t.Fatalf("final %d: %v", i, err)
		}
		if i == 63 && !errors.Is(err, meetings.ErrSink) {
			t.Fatalf("uncertain commit was hidden: %v", err)
		}
	}
	snap := session.Snapshot()
	if snap.State != meetings.Failed || snap.Pending == nil || snap.Pending.Event != last || len(snap.Finals) > 20 {
		t.Fatal("uncertain final or bounded hot tail lost")
	}
	if err := session.Retry(ctx); err != nil {
		t.Fatal(err)
	}
	if snap = session.Snapshot(); snap.State != meetings.Paused || snap.Pending != nil || snap.Finals[len(snap.Finals)-1].Event != last {
		t.Fatal("exact retry failed to recover paused")
	}
	token, err = session.Resume(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Submit(ctx, token, first); err != nil {
		t.Fatal("evicted duplicate rejected by ordering", err)
	}
	if err := session.Pause(ctx); err != nil {
		t.Fatal(err)
	}
	token, err = session.Resume(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := session.Submit(ctx, token, last); !errors.Is(err, meetings.ErrEpoch) {
		t.Fatal("stop accepted stale callback", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	count, cursor := 0, uint64(0)
	var firstSource string
	for {
		page, err := store.MeetingRecords(ctx, metadata.ID, meetings.PageRequest{After: cursor, Limit: 7})
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Records {
			if row.Sequence != uint64(count+1) || row.Record.Event.Text != fmt.Sprintf("Exact recording evidence %03d", count) || row.SourceID == "" {
				t.Fatalf("reopened row %d lost provenance", count)
			}
			if count == 0 {
				firstSource = row.SourceID
			}
			count++
		}
		if !page.More {
			break
		}
		if page.Next <= cursor {
			t.Fatal("cursor stalled")
		}
		cursor = page.Next
	}
	if count != 64 {
		t.Fatalf("retry created or lost evidence: rows=%d", count)
	}
	// A reopened session must consult the same on-disk identity even after a new
	// final advances this process's track clock beyond the old retry.
	session, err = meetings.New(ctx, meetings.Config{SessionID: metadata.ID, Tracks: metadata.Tracks}, store)
	if err != nil {
		t.Fatal(err)
	}
	token, err = session.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	newer := last
	newer.SegmentID = "after-restart"
	newer.Start = 41 * time.Hour
	newer.End = newer.Start + time.Second
	if err := session.Submit(ctx, token, newer); err != nil {
		t.Fatal(err)
	}
	if err := session.Submit(ctx, token, first); err != nil {
		t.Fatal("restart lost durable duplicate", err)
	}
	if err := store.DeleteObservationRecord(ctx, firstSource); err != nil {
		t.Fatal(err)
	}
	if err := session.Submit(ctx, token, first); !errors.Is(err, meetings.ErrDeleted) {
		t.Fatal("deleted evicted key revived after restart", err)
	}
	session, err = meetings.New(ctx, meetings.Config{SessionID: metadata.ID, Tracks: metadata.Tracks}, store)
	if err != nil {
		t.Fatal(err)
	}
	token, err = session.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Submit(ctx, token, newer); err != nil {
		t.Fatal(err)
	}
	last.Text = "Rewritten evidence"
	if err := session.Submit(ctx, token, last); !errors.Is(err, meetings.ErrConflict) {
		t.Fatal("restart lost immutable conflict check", err)
	}
}
