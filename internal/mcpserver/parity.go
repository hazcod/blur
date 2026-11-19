package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Parity tools: read_session, list_wiki, get_time_report and get_promises.
// They are registered from one function so the original tool list stays
// untouched. Each validates its input before the RPC and re-validates the
// response, so an invalid or oversized local reply never reaches the host.

const (
	maxSessionLimit   = 20
	maxSessionWindow  = 24 * time.Hour
	maxWikiListLimit  = 50
	maxReportDays     = 31
	maxPromiseLimit   = 50
	maxPromiseQuery   = 512
	maxReportBuckets  = 16
	maxReportTop      = 20
	maxReportSources  = 8
	maxPromiseSources = 8
)

const readSessionInputSchema = `{"type":"object","additionalProperties":false,"properties":{"app":{"type":"string","minLength":1,"maxLength":128,"description":"App display name (for example Slack) or app ID, case-insensitive."},"from":{"type":"integer","minimum":1,"description":"Window start, Unix seconds."},"to":{"type":"integer","minimum":2,"description":"Window end, Unix seconds, exclusive; at most 24 hours after from."},"limit":{"type":"integer","minimum":1,"maximum":20,"default":10},"after":{"type":"string","pattern":"^[0-9a-f]{64}$","description":"Pass next from the previous page to continue."},"max_bytes_per_source":{"type":"integer","minimum":4,"maximum":4096,"default":2048}},"required":["app","from","to"]}`

const readSessionOutputSchema = `{"type":"object","additionalProperties":false,"properties":{"app_ids":{"type":"array","maxItems":4,"items":{"type":"string","maxLength":128}},"from":{"type":"integer"},"to":{"type":"integer"},"captures":{"type":"array","maxItems":20,"items":{"type":"object","additionalProperties":false,"properties":{"source_id":{"type":"string","pattern":"^[0-9a-f]{64}$"},"app_id":{"type":"string","maxLength":128},"domain":{"type":"string","maxLength":253},"captured_at":{"type":"string","maxLength":40},"last_seen_at":{"type":"string","maxLength":40},"content":{"type":"string","maxLength":4096},"offset_bytes":{"type":"integer","minimum":0},"next_offset_bytes":{"type":"integer","minimum":0},"complete":{"type":"boolean"},"partial_capture":{"type":"boolean"}},"required":["source_id","app_id","last_seen_at","content","offset_bytes","next_offset_bytes","complete"]}},"has_more":{"type":"boolean"},"next":{"type":"string","pattern":"^[0-9a-f]{64}$"},"indexing_pending":{"type":"boolean"},"coverage_limited":{"type":"boolean"}},"required":["app_ids","from","to","captures","has_more"]}`

const listWikiInputSchema = `{"type":"object","additionalProperties":false,"properties":{"type":{"type":"string","enum":["person","project","topic","place","workflow"],"description":"Only list this kind of subject."},"limit":{"type":"integer","minimum":1,"maximum":50,"default":50}}}`

const listWikiOutputSchema = `{"type":"object","additionalProperties":false,"properties":{"entries":{"type":"array","maxItems":50,"items":{"type":"object","additionalProperties":false,"properties":{"id":{"type":"string","maxLength":128},"kind":{"type":"string","enum":["person","project","topic","place","workflow"]},"name":{"type":"string","maxLength":256},"aliases":{"type":"array","maxItems":4,"items":{"type":"string","maxLength":128}},"summary":{"type":"string","maxLength":160},"fact_count":{"type":"integer","minimum":0},"open_loop_count":{"type":"integer","minimum":0}},"required":["id","kind","name","aliases","fact_count","open_loop_count"]}},"total":{"type":"integer","minimum":0},"truncated":{"type":"boolean"}},"required":["entries","total","truncated"]}`

const timeReportInputSchema = `{"type":"object","additionalProperties":false,"properties":{"period":{"type":"string","enum":["today","yesterday","this_week","last_week","last_7_days","last_30_days"],"description":"Named local-calendar period; weeks start Monday. Defaults to today."},"from":{"type":"string","pattern":"^[0-9]{4}-[0-9]{2}-[0-9]{2}$","description":"First local date, inclusive; use with to instead of period."},"to":{"type":"string","pattern":"^[0-9]{4}-[0-9]{2}-[0-9]{2}$","description":"Last local date, inclusive; at most 31 days from from."}}}`

const timeReportOutputSchema = `{"type":"object","additionalProperties":false,"properties":{"from":{"type":"string","maxLength":10},"to":{"type":"string","maxLength":10},"total_seconds":{"type":"integer","minimum":0},"buckets":{"type":"array","maxItems":16,"items":{"type":"object","additionalProperties":false,"properties":{"app_id":{"type":"string","maxLength":128},"name":{"type":"string","maxLength":256},"seconds":{"type":"integer","minimum":0},"by_day":{"type":"object","additionalProperties":{"type":"integer","minimum":0}},"domains":{"type":"array","maxItems":5,"items":{"type":"object","additionalProperties":false,"properties":{"domain":{"type":"string","maxLength":253},"seconds":{"type":"integer","minimum":0}},"required":["domain","seconds"]}}},"required":["app_id","name","seconds","by_day"]}},"omitted_buckets":{"type":"integer","minimum":0},"omitted_bucket_seconds":{"type":"integer","minimum":0},"top_segments":{"type":"array","maxItems":20,"items":{"type":"object","additionalProperties":false,"properties":{"app_id":{"type":"string","maxLength":128},"domain":{"type":"string","maxLength":253},"title":{"type":"string","maxLength":128},"seconds":{"type":"integer","minimum":0},"source_ids":{"type":"array","maxItems":8,"items":{"type":"string","pattern":"^[0-9a-f]{64}$"}}},"required":["seconds","source_ids"]}},"omitted_segments_seconds":{"type":"integer","minimum":0},"note":{"type":"string","maxLength":1024}},"required":["from","to","total_seconds","buckets","top_segments","omitted_segments_seconds","note"]}`

const promisesInputSchema = `{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string","maxLength":512,"description":"Literal, case-insensitive text to find in the item or its subject."},"include_closed":{"type":"boolean","default":false,"description":"Also list completed, cancelled and dismissed items."},"limit":{"type":"integer","minimum":1,"maximum":50,"default":20}}}`

const promisesOutputSchema = `{"type":"object","additionalProperties":false,"properties":{"items":{"type":"array","maxItems":50,"items":{"type":"object","additionalProperties":false,"properties":{"kind":{"type":"string","enum":["open_loop","task","reminder"]},"id":{"type":"string","maxLength":128},"text":{"type":"string","maxLength":600},"state":{"type":"string","maxLength":32},"subject":{"type":"string","maxLength":256},"subject_kind":{"type":"string","maxLength":32},"due_at":{"type":["string","null"],"maxLength":40},"source_ids":{"type":"array","maxItems":8,"items":{"type":"string","pattern":"^[0-9a-f]{64}$"}},"user_authored":{"type":"boolean"},"updated_at":{"type":"string","maxLength":40}},"required":["kind","id","text","state","due_at","source_ids"]}},"has_more":{"type":"boolean"},"coverage_limited":{"type":"boolean"},"note":{"type":"string","maxLength":1024}},"required":["items","has_more","note"]}`

func registerParityTools(server *mcp.Server, reader RPCReader, openWorld *bool) {
	annotations := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: openWorld}
	server.AddTool(&mcp.Tool{
		Name:         "read_session",
		Title:        "Read one app's recent session",
		Description:  "Read the saved captures of one app (for example Slack or a terminal) inside a window of at most 24 hours, oldest first by when each capture was first saved (last_seen_at shows the latest revisit), with each capture's text. A very busy window returns the earliest 512 captures and sets coverage_limited. Use it after get_chronicle or get_recent_activity point at an app and time and you need the actual conversation or session; pass the app's display name or ID plus from/to Unix seconds. Page with next, and open long captures further with get_snapshots using next_offset_bytes. Blur's own interface is never included. Results returned to the MCP host may be sent to its configured model provider.",
		InputSchema:  json.RawMessage(readSessionInputSchema),
		OutputSchema: withFocusProperties(readSessionOutputSchema, "captures"),
		Annotations:  annotations,
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return readSession(ctx, reader, request)
	})
	server.AddTool(&mcp.Tool{
		Name:         "list_wiki",
		Title:        "List Library subjects",
		Description:  "List the people, projects, topics, places and workflows in Blur's Library, best-supported first, with a one-line summary and counts of confirmed facts and open loops. Use it to orient before search_wiki or get_wiki_page when you do not know which subject to look up; it returns no source references, so open a subject with get_wiki_page before relying on details. Results returned to the MCP host may be sent to its configured model provider.",
		InputSchema:  json.RawMessage(listWikiInputSchema),
		OutputSchema: json.RawMessage(listWikiOutputSchema),
		Annotations:  annotations,
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return listWiki(ctx, reader, request)
	})
	server.AddTool(&mcp.Tool{
		Name:         "get_time_report",
		Title:        "Read tracked screen time",
		Description:  "Report tracked foreground screen time for a local-calendar period or date range of at most 31 days, grouped by app and, for browsers, by website, with per-day seconds and the longest segments with source IDs. Use it for questions like how long was I in an app or on a site this week. It is tracked time on this computer, not a timesheet or billable hours, and Blur does not classify time into projects. Results returned to the MCP host may be sent to its configured model provider.",
		InputSchema:  json.RawMessage(timeReportInputSchema),
		OutputSchema: json.RawMessage(timeReportOutputSchema),
		Annotations:  annotations,
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return getTimeReport(ctx, reader, request)
	})
	server.AddTool(&mcp.Tool{
		Name:         "get_promises",
		Title:        "List open follow-ups",
		Description:  "List what the user may still owe or be waiting on: Library open loops plus open tasks and reminders, newest first, each with its subject and source IDs. Use it for what is outstanding, what did I promise, or follow-through questions. Items are observed or user-authored records, never verified outcomes; open the source IDs with get_snapshots before saying something is done or overdue. Results returned to the MCP host may be sent to its configured model provider.",
		InputSchema:  json.RawMessage(promisesInputSchema),
		OutputSchema: json.RawMessage(promisesOutputSchema),
		Annotations:  annotations,
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return getPromises(ctx, reader, request)
	})
}

// callParity runs one validated RPC and decodes the strict response.
func callParity(ctx context.Context, reader RPCReader, method string, params any, output any, maxBytes int) bool {
	if reader == nil || ctx == nil || ctx.Err() != nil {
		return false
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return false
	}
	result, err := reader.Call(ctx, method, encoded)
	defer clear(result)
	return err == nil && len(result) <= maxBytes && decodeJSON(result, output) == nil
}

func parityArguments(request *mcp.CallToolRequest, allowEmpty bool, target any) error {
	if request == nil || request.Params == nil || len(request.Params.Arguments) > maxToolArgsBytes || len(request.Params.Arguments) == 0 && !allowEmpty {
		return ErrUnavailable
	}
	if len(request.Params.Arguments) == 0 {
		return nil
	}
	return decodeJSON(request.Params.Arguments, target)
}

func validSourceIDText(id string) bool {
	return len(id) == 64 && strings.ToLower(id) == id && validHex(id)
}

func validHex(value string) bool {
	for i := range value {
		if c := value[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validOptionalTimestamp(value string) bool {
	if value == "" {
		return true
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return len(value) <= 40 && err == nil
}

// ---- read_session ----

type readSessionInput struct {
	App               string `json:"app"`
	From              int64  `json:"from"`
	To                int64  `json:"to"`
	Limit             *int   `json:"limit,omitempty"`
	After             string `json:"after,omitempty"`
	MaxBytesPerSource *int   `json:"max_bytes_per_source,omitempty"`
}

type sessionCapture struct {
	SourceID        string `json:"source_id"`
	AppID           string `json:"app_id"`
	Domain          string `json:"domain,omitempty"`
	CapturedAt      string `json:"captured_at,omitempty"`
	LastSeenAt      string `json:"last_seen_at"`
	Content         string `json:"content"`
	OffsetBytes     int    `json:"offset_bytes"`
	NextOffsetBytes int    `json:"next_offset_bytes"`
	Complete        bool   `json:"complete"`
	PartialCapture  bool   `json:"partial_capture,omitempty"`

	focusFields // focused-element breadcrumb (focus.go)
}

type readSessionOutput struct {
	AppIDs          []string         `json:"app_ids"`
	From            int64            `json:"from"`
	To              int64            `json:"to"`
	Captures        []sessionCapture `json:"captures"`
	HasMore         bool             `json:"has_more"`
	Next            string           `json:"next,omitempty"`
	IndexingPending bool             `json:"indexing_pending,omitempty"`
	CoverageLimited bool             `json:"coverage_limited,omitempty"`
}

func decodeReadSessionInput(request *mcp.CallToolRequest) (readSessionInput, error) {
	var input readSessionInput
	if parityArguments(request, false, &input) != nil {
		return readSessionInput{}, ErrUnavailable
	}
	input.App = strings.TrimSpace(input.App)
	if !validOutputText(input.App, 128) || input.From <= 0 || input.To <= input.From || time.Duration(input.To-input.From)*time.Second > maxSessionWindow {
		return readSessionInput{}, ErrUnavailable
	}
	if input.Limit != nil && (*input.Limit < 1 || *input.Limit > maxSessionLimit) || input.MaxBytesPerSource != nil && (*input.MaxBytesPerSource < 4 || *input.MaxBytesPerSource > 4096) || input.After != "" && !validSourceIDText(input.After) {
		return readSessionInput{}, ErrUnavailable
	}
	return input, nil
}

func boundReadSessionOutput(output readSessionOutput, input readSessionInput, limit, chunk int) bool {
	if len(output.Captures) > limit || len(output.AppIDs) > 4 || output.From != input.From || output.To != input.To || output.HasMore && !validSourceIDText(output.Next) || !output.HasMore && output.Next != "" || output.HasMore && len(output.Captures) == 0 || output.HasMore && output.Next != output.Captures[len(output.Captures)-1].SourceID {
		return false
	}
	for _, id := range output.AppIDs {
		if !validOutputText(id, 128) || strings.EqualFold(id, appidentity.BundleID) {
			return false
		}
	}
	seen := map[string]bool{}
	for _, item := range output.Captures {
		if !item.valid() {
			return false
		}
		if !validSourceIDText(item.SourceID) || seen[item.SourceID] || !validOutputText(item.AppID, 128) || strings.EqualFold(item.AppID, appidentity.BundleID) || item.Domain != "" && !validOutputText(item.Domain, 253) || !validOptionalTimestamp(item.CapturedAt) || item.LastSeenAt == "" || !validOptionalTimestamp(item.LastSeenAt) || !utf8.ValidString(item.Content) || strings.IndexByte(item.Content, 0) >= 0 || len(item.Content) > chunk || item.OffsetBytes != 0 || item.NextOffsetBytes != len(item.Content) {
			return false
		}
		seen[item.SourceID] = true
	}
	return true
}

func readSession(ctx context.Context, reader RPCReader, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	input, err := decodeReadSessionInput(request)
	if err != nil {
		return toolError("Reading a session needs an app name, a from and to time in Unix seconds no more than 24 hours apart, up to 20 captures, and an optional next cursor."), nil
	}
	limit, chunk := 10, 2048
	if input.Limit != nil {
		limit = *input.Limit
	}
	if input.MaxBytesPerSource != nil {
		chunk = *input.MaxBytesPerSource
	}
	params := localapi.ReadSessionParams{App: input.App, From: input.From, To: input.To, Limit: limit, After: input.After, MaxBytesPerSource: chunk}
	var output readSessionOutput
	if !callParity(ctx, reader, localapi.RPCReadSession, params, &output, localapi.MaxRPCSnapshotResponseBytes) {
		return toolError("Blur local memory is unavailable."), nil
	}
	if !boundReadSessionOutput(output, input, limit, chunk) {
		return toolError("Blur returned an invalid session response."), nil
	}
	message := fmt.Sprintf("Returned %d capture(s) for the session.", len(output.Captures))
	if output.HasMore {
		message += " More captures follow; pass next to continue."
	}
	if output.IndexingPending {
		message += " Search indexing is still catching up, so the window may be incomplete."
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}, StructuredContent: output}, nil
}

// ---- list_wiki ----

type listWikiInput struct {
	Type  string `json:"type,omitempty"`
	Limit *int   `json:"limit,omitempty"`
}

type listWikiEntry struct {
	ID            string   `json:"id"`
	Kind          string   `json:"kind"`
	Name          string   `json:"name"`
	Aliases       []string `json:"aliases"`
	Summary       string   `json:"summary,omitempty"`
	FactCount     int      `json:"fact_count"`
	OpenLoopCount int      `json:"open_loop_count"`
}

type listWikiOutput struct {
	Entries   []listWikiEntry `json:"entries"`
	Total     int             `json:"total"`
	Truncated bool            `json:"truncated"`
}

func listWiki(ctx context.Context, reader RPCReader, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input listWikiInput
	if parityArguments(request, true, &input) != nil || input.Type != "" && !validWikiKind(input.Type) || input.Limit != nil && (*input.Limit < 1 || *input.Limit > maxWikiListLimit) {
		return toolError("Library listing accepts an optional kind (person, project, topic, place or workflow) and up to 50 results."), nil
	}
	limit := maxWikiListLimit
	if input.Limit != nil {
		limit = *input.Limit
	}
	var output listWikiOutput
	if !callParity(ctx, reader, localapi.RPCListWiki, localapi.ListWikiParams{Type: input.Type, Limit: limit}, &output, localapi.MaxRPCSnapshotResponseBytes) {
		return toolError("Blur local memory is unavailable."), nil
	}
	valid := len(output.Entries) <= limit && output.Total >= len(output.Entries)
	seen := map[string]bool{}
	for _, entry := range output.Entries {
		valid = valid && validOpaqueMemoryID(entry.ID) && !seen[entry.ID] && validWikiKind(entry.Kind) && (input.Type == "" || entry.Kind == input.Type) && validOutputText(entry.Name, 256) && len(entry.Aliases) <= 4 && len(entry.Summary) <= 160 && utf8.ValidString(entry.Summary) && entry.FactCount >= 0 && entry.OpenLoopCount >= 0
		for _, alias := range entry.Aliases {
			valid = valid && validOutputText(alias, 128)
		}
		seen[entry.ID] = true
	}
	if !valid {
		return toolError("Blur returned an invalid Library listing."), nil
	}
	message := fmt.Sprintf("Listed %d of %d Library subject(s).", len(output.Entries), output.Total)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}, StructuredContent: output}, nil
}

// ---- get_time_report ----

type timeReportInput struct {
	Period string `json:"period,omitempty"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

type reportDomain struct {
	Domain  string `json:"domain"`
	Seconds int64  `json:"seconds"`
}

type reportBucket struct {
	AppID   string           `json:"app_id"`
	Name    string           `json:"name"`
	Seconds int64            `json:"seconds"`
	ByDay   map[string]int64 `json:"by_day"`
	Domains []reportDomain   `json:"domains,omitempty"`
}

type reportTopSegment struct {
	AppID     string   `json:"app_id,omitempty"`
	Domain    string   `json:"domain,omitempty"`
	Title     string   `json:"title,omitempty"`
	Seconds   int64    `json:"seconds"`
	SourceIDs []string `json:"source_ids"`
}

type timeReportOutput struct {
	From                   string             `json:"from"`
	To                     string             `json:"to"`
	TotalSeconds           int64              `json:"total_seconds"`
	Buckets                []reportBucket     `json:"buckets"`
	OmittedBuckets         int                `json:"omitted_buckets,omitempty"`
	OmittedBucketSeconds   int64              `json:"omitted_bucket_seconds,omitempty"`
	TopSegments            []reportTopSegment `json:"top_segments"`
	OmittedSegmentsSeconds int64              `json:"omitted_segments_seconds"`
	Note                   string             `json:"note"`
}

var timeReportPeriods = map[string]bool{"today": true, "yesterday": true, "this_week": true, "last_week": true, "last_7_days": true, "last_30_days": true}

func parseReportDate(value string) (time.Time, bool) {
	at, err := time.Parse("2006-01-02", value)
	return at, err == nil && len(value) == 10
}

func validTimeReportInput(input timeReportInput) bool {
	custom := input.From != "" || input.To != ""
	if input.Period != "" {
		return !custom && timeReportPeriods[input.Period]
	}
	if !custom {
		return true
	}
	from, okFrom := parseReportDate(input.From)
	to, okTo := parseReportDate(input.To)
	return okFrom && okTo && !to.Before(from) && to.Sub(from) <= time.Duration(maxReportDays-1)*24*time.Hour
}

func boundTimeReportOutput(output timeReportOutput) bool {
	if _, ok := parseReportDate(output.From); !ok || len(output.Buckets) > maxReportBuckets || len(output.TopSegments) > maxReportTop || output.TotalSeconds < 0 || output.OmittedSegmentsSeconds < 0 || output.OmittedBuckets < 0 || output.OmittedBucketSeconds < 0 || !validOutputText(output.Note, 1024) {
		return false
	}
	if _, ok := parseReportDate(output.To); !ok || output.To < output.From {
		return false
	}
	var sum int64
	for _, bucket := range output.Buckets {
		if bucket.AppID != "" && !validOutputText(bucket.AppID, 128) || strings.EqualFold(bucket.AppID, appidentity.BundleID) || !validOutputText(bucket.Name, 256) || bucket.Seconds < 0 || len(bucket.Domains) > 5 || len(bucket.ByDay) > maxReportDays {
			return false
		}
		sum += bucket.Seconds
		for day, seconds := range bucket.ByDay {
			if _, ok := parseReportDate(day); !ok || seconds < 0 || day < output.From || day > output.To {
				return false
			}
		}
		for _, domain := range bucket.Domains {
			if !validOutputText(domain.Domain, 253) || domain.Seconds < 0 {
				return false
			}
		}
	}
	if sum+output.OmittedBucketSeconds > output.TotalSeconds+int64(len(output.Buckets)+output.OmittedBuckets) {
		return false
	}
	for _, top := range output.TopSegments {
		if top.AppID != "" && !validOutputText(top.AppID, 128) || strings.EqualFold(top.AppID, appidentity.BundleID) || top.Domain != "" && !validOutputText(top.Domain, 253) || top.Title != "" && !validOutputText(top.Title, 128) || top.Seconds < 0 || len(top.SourceIDs) > maxReportSources {
			return false
		}
		for _, id := range top.SourceIDs {
			if !validSourceIDText(id) {
				return false
			}
		}
	}
	return true
}

func getTimeReport(ctx context.Context, reader RPCReader, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input timeReportInput
	if parityArguments(request, true, &input) != nil || !validTimeReportInput(input) {
		return toolError("Time report accepts one period (today, yesterday, this_week, last_week, last_7_days or last_30_days) or inclusive from and to dates (YYYY-MM-DD) at most 31 days apart."), nil
	}
	var output timeReportOutput
	if !callParity(ctx, reader, localapi.RPCGetTimeReport, localapi.GetTimeReportParams{Period: input.Period, From: input.From, To: input.To}, &output, localapi.MaxRPCSnapshotResponseBytes) {
		return toolError("Blur local memory is unavailable."), nil
	}
	if !boundTimeReportOutput(output) {
		return toolError("Blur returned an invalid time report."), nil
	}
	message := fmt.Sprintf("Tracked %d second(s) of foreground screen time from %s to %s. This is not a timesheet.", output.TotalSeconds, output.From, output.To)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}, StructuredContent: output}, nil
}

// ---- get_promises ----

type promisesInput struct {
	Query         string `json:"query,omitempty"`
	IncludeClosed bool   `json:"include_closed,omitempty"`
	Limit         *int   `json:"limit,omitempty"`
}

type promiseItem struct {
	Kind         string   `json:"kind"`
	ID           string   `json:"id"`
	Text         string   `json:"text"`
	State        string   `json:"state"`
	Subject      string   `json:"subject,omitempty"`
	SubjectKind  string   `json:"subject_kind,omitempty"`
	DueAt        *string  `json:"due_at"`
	SourceIDs    []string `json:"source_ids"`
	UserAuthored bool     `json:"user_authored,omitempty"`
	UpdatedAt    string   `json:"updated_at,omitempty"`
}

type promisesOutput struct {
	Items           []promiseItem `json:"items"`
	HasMore         bool          `json:"has_more"`
	CoverageLimited bool          `json:"coverage_limited,omitempty"`
	Note            string        `json:"note"`
}

func getPromises(ctx context.Context, reader RPCReader, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input promisesInput
	if parityArguments(request, true, &input) != nil || !utf8.ValidString(input.Query) || strings.IndexByte(input.Query, 0) >= 0 || len(input.Query) > maxPromiseQuery || input.Limit != nil && (*input.Limit < 1 || *input.Limit > maxPromiseLimit) {
		return toolError("Follow-up listing accepts an optional text query up to 512 bytes, include_closed, and up to 50 results."), nil
	}
	limit := 20
	if input.Limit != nil {
		limit = *input.Limit
	}
	var output promisesOutput
	params := localapi.GetPromisesParams{Query: strings.TrimSpace(input.Query), IncludeClosed: input.IncludeClosed, Limit: limit}
	if !callParity(ctx, reader, localapi.RPCGetPromises, params, &output, localapi.MaxRPCSnapshotResponseBytes) {
		return toolError("Blur local memory is unavailable."), nil
	}
	valid := len(output.Items) <= limit && validOutputText(output.Note, 1024)
	for _, item := range output.Items {
		valid = valid && (item.Kind == "open_loop" || item.Kind == "task" || item.Kind == "reminder") && validOutputText(item.ID, 128) && validOutputText(item.Text, 600) && validOutputText(item.State, 32) && (item.Subject == "" || validOutputText(item.Subject, 256)) && len(item.SubjectKind) <= 32 && len(item.SourceIDs) <= maxPromiseSources && validOptionalTimestamp(item.UpdatedAt) && (item.DueAt == nil || validOptionalTimestamp(*item.DueAt) && *item.DueAt != "") && (item.Kind == "reminder") == (item.DueAt != nil)
		for _, id := range item.SourceIDs {
			valid = valid && validSourceIDText(id)
		}
	}
	if !valid {
		return toolError("Blur returned an invalid follow-up listing."), nil
	}
	message := fmt.Sprintf("Listed %d follow-up item(s). These are observed or user-authored records, not verified outcomes.", len(output.Items))
	if output.HasMore {
		message += " More items were omitted by the result limit."
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}, StructuredContent: output}, nil
}
