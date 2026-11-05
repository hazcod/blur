// Package search provides bounded, deterministic exact-term retrieval over sanitized observations.
package search

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nielsgroen/blur/internal/storage"
)

const (
	MaxQueryBytes    = 256
	MaxTerms         = 16
	MaxResults       = 100
	MaxSnippetBytes  = 512
	MaxResponseBytes = 64 << 10
	MaxIndexRows     = 1000
	MaxIndexBytes    = 2 << 20
	// MaxIndexNodes stays within the durable reader's physical transaction
	// operation cap; an exhausted query returns ErrIndexBudget first.
	MaxIndexNodes = 1024
	MaxScan       = 64
	MaxScanBytes  = 2 << 20
	StorePage     = 4
)

var (
	ErrInvalidRequest       = errors.New("search: invalid request")
	ErrUnsupportedFilter    = errors.New("search: unsupported filter")
	ErrScanFallbackDisabled = errors.New("search: scan fallback disabled")
)

// UnsupportedFilterError identifies a valid query filter that this search
// backend cannot evaluate. Its formatting never includes filter values.
type UnsupportedFilterError struct{ Filter string }

func (e *UnsupportedFilterError) Error() string { return "search: unsupported filter" }
func (e *UnsupportedFilterError) Unwrap() error { return ErrUnsupportedFilter }

// TimeRange is reserved for activity-linked retrieval. Observations have no
// authoritative timestamps yet, so a non-empty range is rejected explicitly.
type TimeRange struct {
	Start time.Time `json:"start,omitempty"`
	End   time.Time `json:"end,omitempty"`
}

func (r *TimeRange) empty() bool { return r == nil || r.Start.IsZero() && r.End.IsZero() }

type Request struct {
	Query             string     `json:"query"`
	AppID             string     `json:"app_id,omitempty"`
	Domain            string     `json:"domain,omitempty"`
	TimeRange         *TimeRange `json:"time_range,omitempty"`
	ResultLimit       int        `json:"result_limit"`
	SnippetBytes      int        `json:"snippet_bytes"`
	ResponseBytes     int        `json:"response_bytes"`
	IndexLimit        int        `json:"index_limit,omitempty"`
	IndexBytes        int        `json:"index_bytes,omitempty"`
	ScanLimit         int        `json:"scan_limit"`
	ScanBytes         int        `json:"scan_bytes,omitempty"`
	AllowScanFallback bool       `json:"allow_scan_fallback,omitempty"`
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
	terms, _ := NormalizeTokens(s, len(s)+1)
	return terms
}

// CanonicalQuery returns a unique, sorted term sequence for semantic proof binding.
func CanonicalQuery(query string) (string, error) {
	if len(query) == 0 || len(query) > MaxQueryBytes || !utf8.ValidString(query) || strings.IndexByte(query, 0) >= 0 {
		return "", ErrInvalidRequest
	}
	terms, err := NormalizeTokens(query, MaxTerms)
	if err != nil || len(terms) == 0 {
		return "", ErrInvalidRequest
	}
	return strings.Join(terms, " "), nil
}

// CanonicalAppID and CanonicalDomain use the same bounded ASCII identifier
// rules as capture metadata. App IDs are lowercased; domains also lose one
// trailing root dot. Empty values mean that no filter was requested.
func CanonicalAppID(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > 128 || !utf8.ValidString(raw) || strings.ContainsRune(raw, 0) {
		return "", ErrInvalidRequest
	}
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" || !validIdentifier(value) {
		return "", ErrInvalidRequest
	}
	return value, nil
}

func CanonicalDomain(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > 253 || !utf8.ValidString(raw) || strings.ContainsRune(raw, 0) {
		return "", ErrInvalidRequest
	}
	value := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if value == "" || !validIdentifier(value) || strings.Contains(value, "..") || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return "", ErrInvalidRequest
	}
	return value, nil
}

func validIdentifier(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func hasTimeFilter(r Request) bool { return !r.TimeRange.empty() }

func (r Request) Validate() (string, error) {
	query, err := CanonicalQuery(r.Query)
	_, appErr := CanonicalAppID(r.AppID)
	_, domainErr := CanonicalDomain(r.Domain)
	if r.TimeRange != nil && !r.TimeRange.empty() && (!r.TimeRange.Start.IsZero() && !r.TimeRange.End.IsZero() && !r.TimeRange.Start.Before(r.TimeRange.End)) {
		return "", ErrInvalidRequest
	}
	if err != nil || appErr != nil || domainErr != nil || r.ResultLimit < 1 || r.ResultLimit > MaxResults || r.SnippetBytes < 1 || r.SnippetBytes > MaxSnippetBytes || r.ResponseBytes < 128 || r.ResponseBytes > MaxResponseBytes || r.IndexLimit < 0 || r.IndexLimit > MaxIndexRows || r.IndexBytes < 0 || r.IndexBytes > MaxIndexBytes || r.ScanLimit < 1 || r.ScanLimit > MaxScan || r.ScanBytes < 0 || r.ScanBytes > MaxScanBytes {
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

func (r Request) EffectiveIndexLimit() int {
	if r.IndexLimit == 0 {
		return MaxIndexRows
	}
	return r.IndexLimit
}

func (r Request) EffectiveIndexBytes() int {
	if r.IndexBytes == 0 {
		return MaxIndexBytes
	}
	return r.IndexBytes
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
	if hasTimeFilter(request) {
		return Response{}, &UnsupportedFilterError{Filter: "time"}
	}
	wanted := strings.Fields(canonical)
	appFilter, _ := CanonicalAppID(request.AppID)
	domainFilter, _ := CanonicalDomain(request.Domain)
	if index, ok := e.store.(ExactIndex); ok {
		page, err := index.SearchExact(ctx, ExactQuery{
			Terms: wanted, AppID: appFilter, Domain: domainFilter,
			TopK: request.ResultLimit, MaxRows: request.EffectiveIndexLimit(), MaxBytes: request.EffectiveIndexBytes(),
		})
		if err != nil {
			return Response{}, err
		}
		if page.Rows < 0 || page.Rows > request.EffectiveIndexLimit() || page.Nodes < 0 || page.Nodes > MaxIndexNodes || page.Bytes < 0 || page.Bytes > request.EffectiveIndexBytes() || len(page.Hits) > page.Rows {
			return Response{}, ErrIndexCorrupt
		}
		for _, hit := range page.Hits {
			if !validSourceID(hit.SourceID) || hit.Score <= 0 || hit.Bytes < 0 {
				return Response{}, ErrIndexCorrupt
			}
		}
		sort.Slice(page.Hits, func(i, j int) bool {
			return better(
				Result{Score: page.Hits[i].Score, SourceID: page.Hits[i].SourceID},
				Result{Score: page.Hits[j].Score, SourceID: page.Hits[j].SourceID},
			)
		})
		response := Response{
			Results: make([]Result, 0, min(request.ResultLimit, len(page.Hits))),
			Scanned: page.Rows, ScannedBytes: page.Bytes, Truncated: page.HasMore,
		}
		for _, hit := range page.Hits {
			if err := ctx.Err(); err != nil {
				return Response{}, err
			}
			if hit.Bytes > request.EffectiveIndexBytes()-response.ScannedBytes {
				response.Truncated = true
				break
			}
			observation, found, err := e.store.GetObservation(ctx, hit.SourceID)
			if err != nil {
				return Response{}, err
			}
			text := observation.Text()
			appID := observation.AppID()
			domain := observation.Domain()
			if !found || len(text)+len(appID)+len(domain) != hit.Bytes || termScore(text, appID, domain, wanted) != hit.Score || appFilter != "" && appID != appFilter || domainFilter != "" && domain != domainFilter {
				return Response{}, ErrIndexCorrupt
			}
			response.ScannedBytes += hit.Bytes
			response.Results = append(response.Results, Result{SourceID: hit.SourceID, AppID: appID, Domain: domain, Score: hit.Score, Snippet: snippet(text, request.SnippetBytes), ProvenanceKind: "raw_sanitized_observation"})
			if len(response.Results) == request.ResultLimit {
				break
			}
		}
		response.Omitted = len(page.Hits) - len(response.Results)
		if response.Omitted > 0 {
			response.Truncated = true
		}
		if err := ctx.Err(); err != nil {
			return Response{}, err
		}
		return boundResponse(response, request.ResponseBytes)
	}
	if !request.AllowScanFallback {
		return Response{}, ErrScanFallbackDisabled
	}
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
			if appFilter != "" && appID != appFilter || domainFilter != "" && domain != domainFilter {
				continue
			}
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
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	return boundResponse(response, request.ResponseBytes)
}

func boundResponse(response Response, maxBytes int) (Response, error) {
	// Account for the exact serialized envelope, including escapes and counters.
	for {
		encoded, err := json.Marshal(response)
		if err != nil {
			return Response{}, err
		}
		if len(encoded) <= maxBytes {
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

func validSourceID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for i := range id {
		if !(id[i] >= '0' && id[i] <= '9' || id[i] >= 'a' && id[i] <= 'f') {
			return false
		}
	}
	return true
}
