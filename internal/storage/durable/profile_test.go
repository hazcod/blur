package durable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/profile"
)

func profileFixture(n int) profile.Record {
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	return profile.Record{ID: fmt.Sprintf("%032x", n), Revision: 1, Kind: profile.WritingRule, Text: "Use short, direct sentences for the private project.", Scope: profile.Scope{Project: "Private Atlas"}, Enabled: true, UserAuthored: true, CreatedAt: at, UpdatedAt: at}
}

func TestExplicitProfileEncryptedReopenCorrectionPauseAndDeletion(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xe1)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	r := profileFixture(1)
	if err := s.PutProfile(ctx, r, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProfile(ctx, r, 0); !errors.Is(err, profile.ErrConflict) {
		t.Fatal("duplicate profile write replaced the current revision", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(r.Text)) || bytes.Contains(raw, []byte(r.Scope.Project)) || bytes.Contains(raw, []byte(r.ID)) {
		t.Fatal("profile data is plaintext on disk")
	}
	clear(raw)
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetProfile(ctx, r.ID)
	if err != nil || !found || got.Text != r.Text || !got.UserAuthored {
		t.Fatal(got, found, err)
	}
	got.Revision++
	got.Text = "Use a formal tone for this project."
	got.Enabled = false
	got.UpdatedAt = got.UpdatedAt.Add(time.Minute)
	if err := s.PutProfile(ctx, got, r.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProfile(ctx, r.ID, r.Revision); !errors.Is(err, profile.ErrConflict) {
		t.Fatal("stale view deleted corrected rule", err)
	}
	got, _, _ = s.GetProfile(ctx, r.ID)
	if got.Enabled || got.Text == r.Text {
		t.Fatal("pause/correction was not durable")
	}
	if err := s.DeleteAllProfile(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListProfile(ctx, profile.PageRequest{Limit: 32})
	if err != nil || len(page.Records) != 0 {
		t.Fatal(page, err)
	}
}

func TestProfilePagingDoesNotUseAnotherFeatureDirectory(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xe2), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	for n := 1; n <= 70; n++ {
		if err := s.PutProfile(ctx, profileFixture(n), 0); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	after := ""
	for n := 0; n < 10; n++ {
		page, err := s.ListProfile(ctx, profile.PageRequest{Limit: 13, After: after})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Records {
			if seen[r.ID] {
				t.Fatal("duplicate profile page")
			}
			seen[r.ID] = true
		}
		if !page.HasMore {
			break
		}
		if page.Next == after || page.Next == "" {
			t.Fatal("profile cursor did not advance")
		}
		after = page.Next
	}
	if len(seen) != 70 {
		t.Fatal("profile paging omitted records", len(seen))
	}
}
