// Package search provides bounded, deterministic exact-term retrieval over sanitized observations.
package search

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nielsgroen/blur/internal/storage"
)

const (
	MaxQueryBytes    = 256
	MaxTerms         = 16
	MaxResults       = 100
	MaxSnippetBytes  = 512
	MaxResponseBytes = 64 << 10
	MaxScan          = 64
	MaxScanBytes     = 2 << 20
	StorePage        = 4
)

var ErrInvalidRequest = errors.New("search: invalid request")

type Request struct {
	Query         string `json:"query"`
	ResultLimit   int    `json:"result_limit"`
	SnippetBytes  int    `json:"snippet_bytes"`
	ResponseBytes int    `json:"response_bytes"`
	ScanLimit     int    `json:"scan_limit"`
	ScanBytes     int    `json:"scan_bytes,omitempty"`
}

type Result struct {
	SourceID       string `json:"source_id"`
	AppID          string `json:"app_id"`
	Domain         string `json:"domain"`
	Score          int    `json:"score"`
	Snippet        string `json:"snippet"`
	ProvenanceKind string `json:"provenance_kind"`
}

type Response struct {
	Results      []Result `json:"results"`
	Scanned      int      `json:"scanned"`
	ScannedBytes int      `json:"scanned_bytes"`
	Truncated    bool     `json:"truncated"`
	Omitted      int      `json:"omitted"`
}

type Engine struct{ store storage.EncryptedStore }

func New(store storage.EncryptedStore) *Engine { return &Engine{store: store} }

func tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// CanonicalQuery returns a unique, sorted term sequence for semantic proof binding.
func CanonicalQuery(query string) (string, error) {
	if len(query) == 0 || len(query) > MaxQueryBytes || !utf8.ValidString(query) || strings.IndexByte(query, 0) >= 0 {
		return "", ErrInvalidRequest
	}
	seen := make(map[string]bool, MaxTerms)
	for _, term := range tokens(query) {
		seen[term] = true
		if len(seen) > MaxTerms {
			return "", ErrInvalidRequest
		}
	}
	if len(seen) == 0 {
		return "", ErrInvalidRequest
	}
	unique := make([]string, 0, len(seen))
	for term := range seen {
		unique = append(unique, term)
	}
	sort.Strings(unique)
	return strings.Join(unique, " "), nil
}

func (r Request) Validate() (string, error) {
	query, err := CanonicalQuery(r.Query)
	if err != nil || r.ResultLimit < 1 || r.ResultLimit > MaxResults || r.SnippetBytes < 1 || r.SnippetBytes > MaxSnippetBytes || r.ResponseBytes < 128 || r.ResponseBytes > MaxResponseBytes || r.ScanLimit < 1 || r.ScanLimit > MaxScan || r.ScanBytes < 0 || r.ScanBytes > MaxScanBytes {
		return "", ErrInvalidRequest
	}
	return query, nil
}

// EffectiveScanBytes maps the omitted zero value to the hard maximum, keeping
// older request shapes valid without allowing an unbounded scan.
func (r Request) EffectiveScanBytes() int {
	if r.ScanBytes == 0 {
		return MaxScanBytes
	}
	return r.ScanBytes
}

func termScore(text, appID, domain string, wanted []string) int {
	seen := make(map[string]int, len(wanted))
	for _, field := range []struct {
		s      string
		weight int
	}{{text, 1}, {appID, 3}, {domain, 2}} {
		for _, token := range tokens(field.s) {
			for _, term := range wanted {
				if token == term && field.weight > seen[term] {
					seen[term] = field.weight
				}
			}
		}
	}
	if len(seen) != len(wanted) {
		return 0
	}
	score := 0
	for _, term := range wanted {
		score += seen[term]
	}
	return score
}

func snippet(text string, max int) string {
	if len(text) <= max {
		return text
	}
	for max > 0 && !utf8.RuneStart(text[max]) {
		max--
	}
	return text[:max]
}

func better(a, b Result) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.SourceID < b.SourceID
}

func (e *Engine) Search(ctx context.Context, request Request) (Response, error) {
	canonical, err := request.Validate()
	if err != nil || e == nil || e.store == nil || ctx == nil {
		return Response{}, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	wanted := strings.Fields(canonical)
	scanBytes := request.EffectiveScanBytes()
	response := Response{Results: make([]Result, 0, request.ResultLimit)}
	matched := 0
	after := ""
	for response.Scanned < request.ScanLimit && response.ScannedBytes < scanBytes {
		pageSize := StorePage
		if left := request.ScanLimit - response.Scanned; left < pageSize {
			pageSize = left
		}
		pageBytes := storage.MaxObservationPageBytes
		if left := scanBytes - response.ScannedBytes; left < pageBytes {
			pageBytes = left
		}
		page, err := e.store.ListObservationPage(ctx, storage.ObservationPageRequest{Limit: pageSize, MaxBytes: pageBytes, After: after})
		if err != nil {
			return Response{}, err
		}
		if len(page.Observations) > pageSize || page.Bytes < 0 || page.Bytes > pageBytes {
			return Response{}, ErrInvalidRequest
		}
		if len(page.Observations) == 0 {
			response.Truncated = response.Truncated || page.HasMore
			break
		}
		pageUsedBytes := 0
		for _, o := range page.Observations {
			if err := ctx.Err(); err != nil {
				return Response{}, err
			}
			text := o.Text()
			appID := o.AppID()
			domain := o.Domain()
			observationBytes := len(text) + len(appID) + len(domain)
			if observationBytes > scanBytes-response.ScannedBytes || observationBytes > pageBytes-pageUsedBytes {
				return Response{}, ErrInvalidRequest
			}
			pageUsedBytes += observationBytes
			response.Scanned++
			response.ScannedBytes += observationBytes
			sourceID := o.ContentID()
			after = sourceID
			if score := termScore(text, appID, domain, wanted); score > 0 {
				matched++
				result := Result{SourceID: sourceID, AppID: appID, Domain: domain, Score: score, Snippet: snippet(text, request.SnippetBytes), ProvenanceKind: "raw_sanitized_observation"}
				response.Results = append(response.Results, result)
				sort.Slice(response.Results, func(i, j int) bool { return better(response.Results[i], response.Results[j]) })
				if len(response.Results) > request.ResultLimit {
					response.Results = response.Results[:request.ResultLimit]
				}
			}
		}
		if pageUsedBytes != page.Bytes {
			return Response{}, ErrInvalidRequest
		}
		if !page.HasMore {
			break
		}
		if response.Scanned >= request.ScanLimit || response.ScannedBytes >= scanBytes {
			response.Truncated = true
			break
		}
	}
	response.Omitted = matched - len(response.Results)
	if response.Omitted > 0 {
		response.Truncated = true
	}
	// Account for the exact serialized envelope, including escapes and counters.
	for {
		encoded, err := json.Marshal(response)
		if err != nil {
			return Response{}, err
		}
		if len(encoded) <= request.ResponseBytes {
			break
		}
		if len(response.Results) == 0 {
			return Response{}, ErrInvalidRequest
		}
		response.Results = response.Results[:len(response.Results)-1]
		response.Omitted++
		response.Truncated = true
	}
	return response, nil
}
