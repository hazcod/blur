package browsercapture

import (
	"strings"
	"testing"
	"time"
)

func TestSanitizeAcceptsOnlyCurrentActiveNonPrivateSafariPage(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	page := Snapshot{
		Browser: SafariBundleID, URL: "https://docs.example.test/plan?token=url-secret#private-fragment",
		Title: "Quarterly plan", Text: "Owner: Sam\nPassword: page-secret", Active: true,
		PrivacyKnown: true, CapturedAt: now,
	}
	observation, err := Sanitize(page, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if observation.AppID() != SafariBundleID || observation.Domain() != "docs.example.test" {
		t.Fatalf("observation metadata = (%q, %q)", observation.AppID(), observation.Domain())
	}
	if !strings.Contains(observation.Text(), "Quarterly plan") || !strings.Contains(observation.Text(), "[REDACTED:SECRET]") {
		t.Fatalf("page text was not retained and redacted: %q", observation.Text())
	}
	for _, secret := range []string{"url-secret", "private-fragment", "page-secret"} {
		if strings.Contains(observation.Text(), secret) {
			t.Fatalf("observation retained sensitive input %q", secret)
		}
	}
	if err := observation.Validate(); err != nil {
		t.Fatalf("sanitized page is invalid: %v", err)
	}
}

func TestSanitizeRejectsUnapprovedOrNonCurrentPage(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	base := Snapshot{
		Browser: SafariBundleID, URL: "https://example.test/", Title: "Page", Text: "Body",
		Active: true, PrivacyKnown: true, CapturedAt: now,
	}
	tests := []struct {
		name    string
		page    Snapshot
		enabled bool
	}{
		{"capture disabled", base, false},
		{"unknown browser", withSnapshot(base, func(p *Snapshot) { p.Browser = "org.mozilla.firefox" }), true},
		{"inactive tab", withSnapshot(base, func(p *Snapshot) { p.Active = false }), true},
		{"privacy unknown", withSnapshot(base, func(p *Snapshot) { p.PrivacyKnown = false }), true},
		{"private browsing", withSnapshot(base, func(p *Snapshot) { p.Private = true }), true},
		{"stale page", withSnapshot(base, func(p *Snapshot) { p.CapturedAt = now.Add(-MaxSnapshotAge - time.Second) }), true},
		{"future page", withSnapshot(base, func(p *Snapshot) { p.CapturedAt = now.Add(MaxClockSkew + time.Second) }), true},
		{"non-web scheme", withSnapshot(base, func(p *Snapshot) { p.URL = "file:///Users/example/secret.txt" }), true},
		{"URL credentials", withSnapshot(base, func(p *Snapshot) { p.URL = "https://user:password@example.test/" }), true},
		{"empty body", withSnapshot(base, func(p *Snapshot) { p.Text = " \n\t" }), true},
		{"oversized body", withSnapshot(base, func(p *Snapshot) { p.Text = strings.Repeat("x", MaxPageBytes+1) }), true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Sanitize(test.page, test.enabled, now); err == nil {
				t.Fatal("Sanitize accepted a disallowed page")
			}
		})
	}
}

func withSnapshot(page Snapshot, mutate func(*Snapshot)) Snapshot {
	mutate(&page)
	return page
}
