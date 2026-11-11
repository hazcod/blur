package capture

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/fake"
)

func TestDomainFromDocumentURLKeepsOnlyHTTPHost(t *testing.T) {
	got, ok := domainFromDocumentURL("https://user:secret@Docs.Example.test:8443/path/to/page?token=secret#fragment")
	if !ok || got != "docs.example.test" {
		t.Fatalf("domainFromDocumentURL() = %q, %v; want only docs.example.test", got, ok)
	}
}

func TestBrowserDomainRulesRejectBeforeReadingPageText(t *testing.T) {
	for _, tc := range []struct {
		name        string
		domain      string
		domainKnown bool
		blocked     bool
	}{
		{name: "blocked host", domain: "private.example.test", domainKnown: true, blocked: true},
		{name: "unknown host fails closed", blocked: true},
		{name: "allowed host", domain: "work.example.test", domainKnown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := 0
			p := browserDomainPipeline(tc.domain, tc.domainKnown, &values)
			obs, err := p.Capture(context.Background())
			if tc.blocked {
				var typed *CaptureError
				if !errors.As(err, &typed) || typed.Code != Excluded || values != 0 {
					t.Fatalf("err=%v value reads=%d; want excluded before text", err, values)
				}
				return
			}
			if err != nil || values != 1 || obs.Domain() != "work.example.test" || obs.Text() != "active page" {
				t.Fatalf("obs domain=%q text=%q err=%v value reads=%d", obs.Domain(), obs.Text(), err, values)
			}
		})
	}
}

func TestIncludedBrowserDomainIsEvaluatedAfterHostDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name        string
		domain      string
		domainKnown bool
		allowed     bool
	}{
		{name: "included host", domain: "work.example.test", domainKnown: true, allowed: true},
		{name: "unknown host", allowed: false},
		{name: "different host", domain: "personal.example.test", domainKnown: true, allowed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			p := browserDomainPipeline(tc.domain, tc.domainKnown, &reads)
			p.Exclusions = Exclusions{DefaultDisabled: true, IncludedDomains: []string{"work.example.test"}}
			obs, err := p.Capture(context.Background())
			if !tc.allowed {
				var typed *CaptureError
				if !errors.As(err, &typed) || typed.Code != Excluded || reads != 0 {
					t.Fatalf("err=%v value reads=%d; want excluded before text", err, reads)
				}
				return
			}
			if err != nil || reads != 1 || obs.Domain() != "work.example.test" {
				t.Fatalf("domain=%q err=%v value reads=%d", obs.Domain(), err, reads)
			}
		})
	}
}

func TestBrowserDomainChangeIsRejectedBeforeReadingPageText(t *testing.T) {
	reads := 0
	p := browserDomainPipeline("work.example.test", true, &reads)
	p.Read = func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
		return rawFrame{
			domain: "work.example.test", domainKnown: true,
			validateDomain: func(context.Context, string) error { return errCaptureDomainChanged },
			roots: []*rawNode{{declaredBytes: 11, value: func(context.Context, int) (string, error) {
				reads++
				return "active page", nil
			}}},
		}, nil
	}
	_, err := p.Capture(context.Background())
	var typed *CaptureError
	if !errors.As(err, &typed) || typed.Code != Excluded || reads != 0 {
		t.Fatalf("err=%v value reads=%d; want excluded before text", err, reads)
	}
}

func browserDomainPipeline(domain string, known bool, valueReads *int) Pipeline {
	return Pipeline{
		Source:     &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "browser-window", AppID: "com.apple.Safari"}},
		Limits:     Limits{MaxBytes: 256, MaxNodes: 3, MaxDepth: 2, MaxDuration: time.Second},
		Exclusions: Exclusions{Domains: []string{"private.example.test"}},
		Read: func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
			return rawFrame{
				domain:      domain,
				domainKnown: known,
				roots: []*rawNode{{declaredBytes: len("active page"), value: func(context.Context, int) (string, error) {
					*valueReads++
					return "active page", nil
				}}},
			}, nil
		},
	}
}

func TestDomainFromDocumentURLRejectsNonWebOrMalformedDocuments(t *testing.T) {
	for _, raw := range []string{
		"file:///Users/person/private.txt",
		"ftp://files.example.test/document",
		"https://",
		"https://bad host.test/path",
		"https://" + strings.Repeat("a", 2049) + ".test",
	} {
		t.Run("invalid", func(t *testing.T) {
			if got, ok := domainFromDocumentURL(raw); ok || got != "" {
				t.Fatalf("domainFromDocumentURL(%q) = %q, %v; want empty, false", raw, got, ok)
			}
		})
	}
}
