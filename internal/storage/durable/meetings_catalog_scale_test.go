package durable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/hazcod/blur/internal/meetings"
)

func TestMeetingCatalogPreservesMoreThan4096EncryptedSessionsAndOldCursors(t *testing.T) {
	ctx := context.Background()
	s, path, key := meetingStore(t)
	const count = 4100
	for i := 0; i < count; i++ {
		m := fixtureMeeting(fmt.Sprintf("voice-catalog-%04d", i))
		if err := s.PutMeeting(ctx, m, 0); err != nil {
			t.Fatalf("session %d was rejected from the durable catalog: %v", i+1, err)
		}
	}
	first, err := s.ListMeetings(ctx, meetings.PageRequest{Limit: 2})
	if err != nil || len(first.Meetings) != 2 || first.Meetings[0].ID != "voice-catalog-4099" || first.Meetings[1].ID != "voice-catalog-4098" || !first.More {
		t.Fatalf("large catalog first page missing: %+v err=%v", first, err)
	}
	oldCursor := first.Next
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"voice-catalog-0000", "voice-catalog-4099", "Private launch planning"} {
		if bytes.Contains(raw, []byte(plain)) {
			t.Fatalf("catalog metadata was stored in plaintext: %q", plain)
		}
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	seen, after := 0, uint64(0)
	for {
		page, err := s.ListMeetings(ctx, meetings.PageRequest{After: after})
		if err != nil {
			t.Fatal("large reopened catalog pagination failed", err)
		}
		for _, m := range page.Meetings {
			want := fmt.Sprintf("voice-catalog-%04d", count-1-seen)
			if m.ID != want {
				t.Fatalf("catalog row %d: got %s, want %s", seen, m.ID, want)
			}
			seen++
		}
		if !page.More {
			break
		}
		if page.Next >= after && after != 0 || page.Next == 0 {
			t.Fatalf("catalog cursor did not advance: before=%d next=%d", after, page.Next)
		}
		after = page.Next
	}
	if seen != count {
		t.Fatalf("reopened catalog returned %d sessions, want %d", seen, count)
	}
	// Remove both the cursor's original row and its next row. The ordinal
	// cursor must keep its meaning across holes and later creations.
	for _, id := range []string{"voice-catalog-4098", "voice-catalog-4097"} {
		if err := s.DeleteMeeting(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutMeeting(ctx, fixtureMeeting("voice-catalog-new"), 0); err != nil {
		t.Fatal("creation after deleting catalog holes failed", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListMeetings(ctx, meetings.PageRequest{After: oldCursor, Limit: 2})
	if err != nil || len(page.Meetings) != 2 || page.Meetings[0].ID != "voice-catalog-4096" || page.Meetings[1].ID != "voice-catalog-4095" || !page.More {
		t.Fatalf("old cursor changed after deletion and reopen: %+v err=%v", page, err)
	}
	for _, id := range []string{"voice-catalog-4098", "voice-catalog-4097"} {
		if _, found, err := s.GetMeeting(ctx, id); err != nil || found {
			t.Fatalf("deleted catalog row reappeared: id=%s found=%v err=%v", id, found, err)
		}
	}
}

func TestMeetingCatalogRetainsOrdinalRepresentationGuards(t *testing.T) {
	ctx := context.Background()
	t.Run("last representable ordinal", func(t *testing.T) {
		s, _, _ := meetingStore(t)
		if err := s.Update(ctx, func(tx *Tx) error {
			return tx.PutIndexRecord(meetingMetaNS, "directory", mustJSON(meetingDirectory{Last: 1<<48 - 2}))
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.PutMeeting(ctx, fixtureMeeting("last-ordinal"), 0); err != nil {
			t.Fatal("last representable ordinal was rejected", err)
		}
		if err := s.PutMeeting(ctx, fixtureMeeting("ordinal-overflow"), 0); !errors.Is(err, meetings.ErrLimit) {
			t.Fatalf("unrepresentable catalog ordinal was accepted: %v", err)
		}
		if _, found, err := s.GetMeeting(ctx, "ordinal-overflow"); err != nil || found {
			t.Fatalf("overflow failure left a new session: found=%v err=%v", found, err)
		}
	})
	for _, tc := range []struct {
		name string
		dir  meetingDirectory
	}{
		{"unrepresentable last", meetingDirectory{Last: 1 << 48}},
		{"live exceeds assigned ordinals", meetingDirectory{Last: 1, Live: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := meetingStore(t)
			if err := s.Update(ctx, func(tx *Tx) error {
				return tx.PutIndexRecord(meetingMetaNS, "directory", mustJSON(tc.dir))
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ListMeetings(ctx, meetings.PageRequest{}); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("invalid catalog directory was accepted: %v", err)
			}
		})
	}
}
