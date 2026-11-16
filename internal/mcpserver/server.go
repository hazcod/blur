// Package mcpserver exposes a deliberately small, read-only MCP surface over
// Blur's authenticated local API. It never opens the encrypted database.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxSearchQueryBytes  = 512
	defaultSearchLimit   = 5
	maxSearchResults     = 8
	maxSearchTextBytes   = 2048
	maxSearchOutputBytes = 16 << 10
	maxStdioLineBytes    = 64 << 10
	maxToolArgsBytes     = 2 << 10
)

var ErrUnavailable = errors.New("blur local memory unavailable")

// RPCReader is the only dependency of the MCP process. Its implementation is
// the bounded client for the encrypted-store owner process.
type RPCReader interface {
	Call(context.Context, string, json.RawMessage) (json.RawMessage, error)
}

type searchMemoryInput struct {
	Query string `json:"query"`
	Limit *int   `json:"limit,omitempty"`
}

type searchMemoryOutput struct {
	IndexingPending bool              `json:"indexing_pending,omitempty"`
	CoverageLimited bool              `json:"coverage_limited,omitempty"`
	Results         []searchMemoryHit `json:"results"`
	Truncated       bool              `json:"truncated"`
}

type searchMemoryHit struct {
	TimeUnknown bool   `json:"capture_time_unknown,omitempty"`
	CapturedAt  string `json:"captured_at,omitempty"`
	Partial     bool   `json:"partial_capture,omitempty"`
	SourceID    string `json:"source_id"`
	AppID       string `json:"app_id"`
	Domain      string `json:"domain,omitempty"`
	Text        string `json:"text"`
	Truncated   bool   `json:"truncated,omitempty"`
}

const searchInputSchema = `{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string","minLength":1,"maxLength":512,"description":"A natural-language memory query; app, website, and time phrases narrow results. Quote text or code for literal matches."},"limit":{"type":"integer","minimum":1,"maximum":8,"description":"Maximum results to return; defaults to 5."}},"required":["query"]}`

const searchOutputSchema = `{"type":"object","additionalProperties":false,"properties":{"results":{"type":"array","maxItems":8,"items":{"type":"object","additionalProperties":false,"properties":{"source_id":{"type":"string","maxLength":64},"app_id":{"type":"string","maxLength":128},"domain":{"type":"string","maxLength":253},"text":{"type":"string","maxLength":2048},"truncated":{"type":"boolean"},"captured_at":{"type":"string","maxLength":40,"format":"date-time"},"partial_capture":{"type":"boolean"},"capture_time_unknown":{"type":"boolean"}},"required":["source_id","app_id","text"]}},"truncated":{"type":"boolean"},"indexing_pending":{"type":"boolean"},"coverage_limited":{"type":"boolean"}},"required":["results","truncated"]}`

const snapshotInputSchema = `{"type":"object","additionalProperties":false,"properties":{"source_ids":{"type":"array","items":{"type":"string","pattern":"^[0-9a-f]{64}$"},"minItems":1,"maxItems":3,"uniqueItems":true,"description":"Source IDs from search_memory results."},"offsets":{"type":"object","additionalProperties":{"type":"integer","minimum":0},"description":"Optional byte offset for each source_id, using next_offset_bytes from the prior response."},"max_bytes_per_source":{"type":"integer","minimum":4,"maximum":4096,"default":4096}},"required":["source_ids"]}`

const snapshotOutputSchema = `{"type":"object","additionalProperties":false,"properties":{"snapshots":{"type":"array","maxItems":3,"items":{"type":"object","additionalProperties":false,"properties":{"source_id":{"type":"string","maxLength":64},"app_id":{"type":"string","maxLength":128},"domain":{"type":"string","maxLength":253},"captured_at":{"type":"string","maxLength":40},"content":{"type":"string","maxLength":4096},"offset_bytes":{"type":"integer","minimum":0},"next_offset_bytes":{"type":"integer","minimum":0},"complete":{"type":"boolean"},"partial_capture":{"type":"boolean"},"document_id":{"type":"string","maxLength":64},"part_index":{"type":"integer","minimum":0},"part_count":{"type":"integer","minimum":1},"document_offset_bytes":{"type":"integer","minimum":0},"document_total_bytes":{"type":"integer","minimum":0}},"required":["source_id","app_id","content","offset_bytes","next_offset_bytes","complete"]}},"truncated":{"type":"boolean"}},"required":["snapshots","truncated"]}`

const chronicleInputSchema = `{"type":"object","additionalProperties":false,"properties":{"level":{"type":"string","enum":["hour","day","week"],"description":"Recap level. hour (default) needs hour_id; day and week need period."},"hour_id":{"type":"string","pattern":"^hour:[0-9]{8}T[0-9]{2}Z$","description":"Stable hour ID from the capture timestamp, formatted hour:YYYYMMDDTHHZ. Only for level hour."},"period":{"type":"string","pattern":"^([0-9]{4}-[0-9]{2}-[0-9]{2}|[0-9]{4}-W[0-9]{2})$","description":"Local calendar day YYYY-MM-DD for level day, or ISO week YYYY-Www for level week."}}}`

const chronicleOutputSchema = `{"type":"object","additionalProperties":false,"properties":{"found":{"type":"boolean"},"hour_id":{"type":"string","maxLength":18},"start":{"type":"string","format":"date-time"},"end":{"type":"string","format":"date-time"},"summary":{"type":"string","maxLength":4096},"source_ids":{"type":"array","maxItems":64,"items":{"type":"string","pattern":"^[0-9a-f]{64}$"}},"follow_up":{"type":"object","additionalProperties":false,"properties":{"source_id":{"type":"string","pattern":"^[0-9a-f]{64}$"},"text":{"type":"string","maxLength":512}},"required":["source_id","text"]},"level":{"type":"string","enum":["day","week"]},"period":{"type":"string","maxLength":10},"text":{"type":"string","maxLength":2048},"child_ids":{"type":"array","maxItems":64,"items":{"type":"string","maxLength":18}},"source_ids_truncated":{"type":"boolean"},"generated_at":{"type":"string","format":"date-time"},"dirty":{"type":"boolean"}},"required":["found"]}`

const chronicleRangeInputSchema = `{"type":"object","additionalProperties":false,"properties":{"start":{"type":"string","format":"date-time","description":"Inclusive RFC3339 start time."},"end":{"type":"string","format":"date-time","description":"Exclusive RFC3339 end time, at most 32 days after start."},"after":{"type":"string","pattern":"^hour:[0-9]{8}T[0-9]{2}Z$","description":"Continue after the last hour_id returned by the previous page."},"limit":{"type":"integer","minimum":1,"maximum":8,"default":8}},"required":["start","end"]}`

const chronicleRangeOutputSchema = `{"type":"object","additionalProperties":false,"properties":{"items":{"type":"array","maxItems":8,"items":{"type":"object","additionalProperties":false,"properties":{"hour_id":{"type":"string","pattern":"^hour:[0-9]{8}T[0-9]{2}Z$"},"start":{"type":"string","format":"date-time"},"end":{"type":"string","format":"date-time"},"summary":{"type":"string","maxLength":4096},"source_ids":{"type":"array","maxItems":64,"items":{"type":"string","pattern":"^[0-9a-f]{64}$"}},"follow_up":{"type":"object","additionalProperties":false,"properties":{"source_id":{"type":"string","pattern":"^[0-9a-f]{64}$"},"text":{"type":"string","maxLength":512}},"required":["source_id","text"]}},"required":["hour_id","start","end","summary","source_ids"]}},"next":{"type":"string","pattern":"^hour:[0-9]{8}T[0-9]{2}Z$"},"truncated":{"type":"boolean"}},"required":["items","truncated"]}`

const recentActivityInputSchema = `{"type":"object","additionalProperties":false,"properties":{"days_back":{"type":"integer","minimum":1,"maximum":7,"default":1},"limit":{"type":"integer","minimum":1,"maximum":8,"default":5,"description":"Maximum recent activity blocks to return."}}}`

const recentActivityOutputSchema = `{"type":"object","additionalProperties":false,"properties":{"segments":{"type":"array","maxItems":8,"items":{"type":"object","additionalProperties":false,"properties":{"start":{"type":"string","format":"date-time"},"end":{"type":"string","format":"date-time"},"duration_ns":{"type":"integer","minimum":1},"app_id":{"type":"string","maxLength":128},"domain":{"type":"string","maxLength":253},"project":{"type":"string","maxLength":128},"topic":{"type":"string","maxLength":128},"source_ids":{"type":"array","maxItems":32,"items":{"type":"string","pattern":"^[0-9a-f]{64}$"}}},"required":["start","end","duration_ns","source_ids"]}},"truncated":{"type":"boolean"}},"required":["segments","truncated"]}`

const workingMemoryInputSchema = `{"type":"object","additionalProperties":false,"properties":{"hours":{"type":"integer","minimum":1,"maximum":6,"default":4,"description":"How far back to look for recent captures."},"limit":{"type":"integer","minimum":1,"maximum":8,"default":5,"description":"Maximum recent captures to return."}}}`

const workingMemoryOutputSchema = `{"type":"object","additionalProperties":false,"properties":{"start":{"type":"string","format":"date-time"},"end":{"type":"string","format":"date-time"},"items":{"type":"array","maxItems":8,"items":{"type":"object","additionalProperties":false,"properties":{"source_id":{"type":"string","pattern":"^[0-9a-f]{64}$"},"app_id":{"type":"string","maxLength":128},"domain":{"type":"string","maxLength":253},"captured_at":{"type":"string","format":"date-time"},"content":{"type":"string","maxLength":1600},"truncated":{"type":"boolean"}},"required":["source_id","app_id","captured_at","content","truncated"]}},"truncated":{"type":"boolean"},"indexing_pending":{"type":"boolean"}},"required":["start","end","items","truncated"]}`

const wikiInputSchema = `{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string","minLength":1,"maxLength":512,"description":"Person, project, topic, workflow or place; aliases and saved facts are searched."},"limit":{"type":"integer","minimum":1,"maximum":8,"default":5}},"required":["query"]}`

const wikiOutputSchema = `{"type":"object","additionalProperties":false,"properties":{"entities":{"type":"array","maxItems":8,"items":{"type":"object","additionalProperties":false,"properties":{"id":{"type":"string","maxLength":128},"kind":{"type":"string","enum":["person","project","topic","place","workflow"]},"name":{"type":"string","maxLength":4096},"aliases":{"type":"array","maxItems":8,"items":{"type":"string","maxLength":4096}},"state":{"type":"string","enum":["confirmed","deterministic","proposed","dismissed","merged"]},"details_truncated":{"type":"boolean"},"facts":{"type":"array","maxItems":6,"items":{"type":"object","additionalProperties":false,"properties":{"text":{"type":"string","maxLength":1200},"text_truncated":{"type":"boolean"},"state":{"type":"string","enum":["confirmed","deterministic","proposed","dismissed","merged"]},"sources_truncated":{"type":"boolean"},"sources":{"type":"array","maxItems":4,"items":{"type":"object","additionalProperties":false,"properties":{"id":{"type":"string","maxLength":64},"user_authored":{"type":"boolean"}}}}},"required":["text","state"]}},"open_loops":{"type":"array","maxItems":6,"items":{"type":"object","additionalProperties":false,"properties":{"text":{"type":"string","maxLength":1200},"text_truncated":{"type":"boolean"},"state":{"type":"string","enum":["confirmed","deterministic","proposed","dismissed","merged"]},"sources_truncated":{"type":"boolean"},"sources":{"type":"array","maxItems":4,"items":{"type":"object","additionalProperties":false,"properties":{"id":{"type":"string","maxLength":64},"user_authored":{"type":"boolean"}}}}},"required":["text","state"]}}},"required":["id","kind","name","state"]}},"truncated":{"type":"boolean"}},"required":["entities","truncated"]}`

const taskInputSchema = `{"type":"object","additionalProperties":false,"properties":{"state":{"type":"string","enum":["open","completed","cancelled","all"],"default":"open"},"limit":{"type":"integer","minimum":1,"maximum":8,"default":5},"after":{"type":"string","pattern":"^[0-9a-f]{32}$","description":"Cursor returned by the previous page."}}}`

const wikiPageInputSchema = `{"type":"object","additionalProperties":false,"properties":{"id":{"type":"string","maxLength":128,"description":"Subject ID returned by search_wiki."},"offset":{"type":"integer","minimum":0,"maximum":256,"default":0},"limit":{"type":"integer","minimum":1,"maximum":4,"default":4}},"required":["id"]}`

const wikiPageOutputSchema = `{"type":"object","additionalProperties":false,"properties":{"found":{"type":"boolean"},"entity":{"type":"object","additionalProperties":false,"properties":{"id":{"type":"string","maxLength":128},"kind":{"type":"string","enum":["person","project","topic","place","workflow"]},"name":{"type":"string","maxLength":4096},"aliases":{"type":"array","maxItems":8,"items":{"type":"string","maxLength":256}},"aliases_truncated":{"type":"boolean"},"state":{"type":"string","enum":["confirmed","deterministic","proposed","dismissed","merged"]},"related_ids":{"type":"array","maxItems":8,"items":{"type":"string","maxLength":128}},"relations_omitted":{"type":"boolean"}},"required":["id","kind","name","state"]},"details":{"type":"array","maxItems":4,"items":{"type":"object","additionalProperties":false,"properties":{"type":{"type":"string","enum":["fact","open_loop"]},"id":{"type":"string","maxLength":128},"text":{"type":"string","maxLength":1200},"text_truncated":{"type":"boolean"},"state":{"type":"string","enum":["confirmed","deterministic","proposed","dismissed","merged"]},"sources":{"type":"array","maxItems":4,"items":{"type":"object","additionalProperties":false,"properties":{"id":{"type":"string","pattern":"^[0-9a-f]{64}$"},"user_authored":{"type":"boolean"}}}},"sources_truncated":{"type":"boolean"}},"required":["type","id","text","state","sources"]}},"offset":{"type":"integer","minimum":0},"next_offset":{"type":"integer","minimum":1,"maximum":256},"has_more":{"type":"boolean"}},"required":["found","details","offset","has_more"]}`

const taskOutputSchema = `{"type":"object","additionalProperties":false,"properties":{"items":{"type":"array","maxItems":8,"items":{"type":"object","additionalProperties":false,"properties":{"id":{"type":"string","pattern":"^[0-9a-f]{32}$"},"revision":{"type":"integer","minimum":1},"title":{"type":"string","maxLength":512},"state":{"type":"string","enum":["open","completed","cancelled"]},"created_at":{"type":"string","format":"date-time"},"updated_at":{"type":"string","format":"date-time"},"source_ids":{"type":"array","maxItems":8,"items":{"type":"string","pattern":"^[0-9a-f]{64}$"}},"user_authored":{"type":"boolean"},"delivery_state":{"type":"string","enum":["needs_sync","scheduling","scheduled","delivered","elapsed","blocked","failed"]},"reminder":{"type":"object","additionalProperties":false,"properties":{"due_at":{"type":"string","format":"date-time"},"repeat":{"type":"string","enum":["","daily","weekly"]},"hour":{"type":"integer","minimum":0,"maximum":23},"minute":{"type":"integer","minimum":0,"maximum":59},"weekday":{"type":"integer","minimum":0,"maximum":6}},"required":["due_at"]},"history":{"type":"array","maxItems":4,"items":{"type":"object","additionalProperties":false,"properties":{"state":{"type":"string","enum":["open","completed","cancelled"]},"at":{"type":"string","format":"date-time"},"revision":{"type":"integer","minimum":1}},"required":["state","at","revision"]}},"history_omitted":{"type":"integer","minimum":0},"history_truncated":{"type":"boolean"}},"required":["id","revision","title","state","created_at","updated_at"]}},"next":{"type":"string","pattern":"^[0-9a-f]{32}$"},"has_more":{"type":"boolean"}},"required":["items","has_more"]}`

// New creates a server with bounded read-only tools and no resources or
// prompts. Tool results are explicitly returned to the MCP host, which may
// forward them to its configured model provider.
func New(reader RPCReader) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "blur", Title: "Blur local memory", Version: "0.1.0"}, nil)
	openWorld := false
	server.AddTool(&mcp.Tool{
		Name:         "get_snapshots",
		Title:        "Read saved capture text",
		Description:  "Read full saved capture text for source IDs returned by search_memory. Large captures are paginated by UTF-8 byte offsets; pass next_offset_bytes in offsets to continue. Each result may contain full text from a source part of a larger document. Results are returned to the MCP host and may be sent to its configured model provider.",
		InputSchema:  json.RawMessage(snapshotInputSchema),
		OutputSchema: json.RawMessage(snapshotOutputSchema),
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &openWorld},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return getSnapshots(ctx, reader, request)
	})
	server.AddTool(&mcp.Tool{
		Name:         "get_chronicle",
		Title:        "Read an hourly, daily or weekly recap",
		Description:  "Read a saved recap. Level hour (the default) reads the hourly recap for a stable hour ID. Level day reads the recap of one local calendar day (period YYYY-MM-DD), and level week the recap of one ISO week (period YYYY-Www); day and week recaps list the hours or days they summarize, and may be marked dirty while newer activity awaits regeneration. Every recap includes the exact capture source IDs it was based on; call get_snapshots before making exact claims. Results are returned to the MCP host and may be sent to its configured model provider.",
		InputSchema:  json.RawMessage(chronicleInputSchema),
		OutputSchema: json.RawMessage(chronicleOutputSchema),
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &openWorld},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return getChronicle(ctx, reader, request)
	})
	server.AddTool(&mcp.Tool{
		Name:         "get_chronicles",
		Title:        "Read recaps across a time range",
		Description:  "Read source-linked hourly recaps across a date range, up to 32 days. Use this to build a day or week overview; continue with the returned next hour ID when truncated. Each recap includes its exact capture source IDs; call get_snapshots before making exact claims. Results are returned to the MCP host and may be sent to its configured model provider.",
		InputSchema:  json.RawMessage(chronicleRangeInputSchema),
		OutputSchema: json.RawMessage(chronicleRangeOutputSchema),
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &openWorld},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return getChronicles(ctx, reader, request)
	})
	server.AddTool(&mcp.Tool{
		Name:         "get_recent_activity",
		Title:        "Read recent activity",
		Description:  "Read a bounded view of recent local activity by day. Results are newest first and include app, site, topic/project labels, and captured source IDs. Use get_snapshots on cited IDs before making claims about exact on-screen content. Results are returned to the MCP host and may be sent to its configured model provider.",
		InputSchema:  json.RawMessage(recentActivityInputSchema),
		OutputSchema: json.RawMessage(recentActivityOutputSchema),
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &openWorld},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return getRecentActivity(ctx, reader, request)
	})
	server.AddTool(&mcp.Tool{
		Name:         "get_working_memory",
		Title:        "Read recent context",
		Description:  "Read the newest saved captures from the last few hours, excluding Blur's own interface. Use this for vague/current questions; each excerpt has a source ID and may be shortened, so call get_snapshots before exact claims. Results are returned to the MCP host and may be sent to its configured model provider.",
		InputSchema:  json.RawMessage(workingMemoryInputSchema),
		OutputSchema: json.RawMessage(workingMemoryOutputSchema),
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &openWorld},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return getWorkingMemory(ctx, reader, request)
	})
	server.AddTool(&mcp.Tool{
		Name:         "search_wiki",
		Title:        "Search people and topics",
		Description:  "Search Blur's Library for people, projects, topics, workflows and places, including saved aliases, facts and open loops. Each fact or open loop includes source references; open the cited captures before stating exact details. User-authored facts are marked explicitly. Results are returned to the MCP host and may be sent to its configured model provider.",
		InputSchema:  json.RawMessage(wikiInputSchema),
		OutputSchema: json.RawMessage(wikiOutputSchema),
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &openWorld},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return searchWiki(ctx, reader, request)
	})
	server.AddTool(&mcp.Tool{
		Name:         "get_wiki_page",
		Title:        "Read a Library subject",
		Description:  "Read the facts and open loops for a specific person, project, topic, workflow or place returned by search_wiki. Details are paginated; pass next_offset to continue. Every detail includes source references, and exact claims should be checked against those captures. Results are returned to the MCP host and may be sent to its configured model provider.",
		InputSchema:  json.RawMessage(wikiPageInputSchema),
		OutputSchema: json.RawMessage(wikiPageOutputSchema),
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &openWorld},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return getWikiPage(ctx, reader, request)
	})
	server.AddTool(&mcp.Tool{
		Name:         "list_tasks",
		Title:        "List tasks and reminders",
		Description:  "Read explicit Blur tasks and reminders. Defaults to open work, supports completed/cancelled history, and returns source IDs for tasks derived from captures. This read does not create, complete, reschedule, or deliver any task. Reminder timestamps include their UTC offset; the private timezone setting is not returned. Results are sent to the MCP host and may be forwarded to its configured model provider.",
		InputSchema:  json.RawMessage(taskInputSchema),
		OutputSchema: json.RawMessage(taskOutputSchema),
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &openWorld},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return listTasks(ctx, reader, request)
	})
	server.AddTool(&mcp.Tool{
		Name:         "search_memory",
		Title:        "Search Blur memory",
		Description:  "Search locally saved captures, notes, tasks and reminders by topic, app, website or time. Natural-language queries use exact text and configured local meaning search; quoted text and code stay literal. Results are returned to the MCP host and may be sent to its configured model provider.",
		InputSchema:  json.RawMessage(searchInputSchema),
		OutputSchema: json.RawMessage(searchOutputSchema),
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &openWorld},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return searchMemory(ctx, reader, request)
	})
	return server
}

type chronicleInput struct {
	HourID string `json:"hour_id,omitempty"`
	Level  string `json:"level,omitempty"`
	Period string `json:"period,omitempty"`
}

type chronicleFollowUp struct {
	SourceID string `json:"source_id"`
	Text     string `json:"text"`
}

type chronicleOutput struct {
	Found     bool               `json:"found"`
	HourID    string             `json:"hour_id,omitempty"`
	Start     string             `json:"start,omitempty"`
	End       string             `json:"end,omitempty"`
	Summary   string             `json:"summary,omitempty"`
	SourceIDs []string           `json:"source_ids,omitempty"`
	FollowUp  *chronicleFollowUp `json:"follow_up,omitempty"`
	// Day and week recaps.
	Level              string   `json:"level,omitempty"`
	Period             string   `json:"period,omitempty"`
	Text               string   `json:"text,omitempty"`
	ChildIDs           []string `json:"child_ids,omitempty"`
	SourceIDsTruncated bool     `json:"source_ids_truncated,omitempty"`
	GeneratedAt        string   `json:"generated_at,omitempty"`
	Dirty              bool     `json:"dirty,omitempty"`
}

type chronicleRangeInput struct {
	Start string `json:"start"`
	End   string `json:"end"`
	After string `json:"after,omitempty"`
	Limit *int   `json:"limit,omitempty"`
}

type chronicleRangeItem struct {
	HourID    string             `json:"hour_id"`
	Start     string             `json:"start"`
	End       string             `json:"end"`
	Summary   string             `json:"summary"`
	SourceIDs []string           `json:"source_ids"`
	FollowUp  *chronicleFollowUp `json:"follow_up,omitempty"`
}

type chronicleRangeOutput struct {
	Items     []chronicleRangeItem `json:"items"`
	Next      string               `json:"next,omitempty"`
	Truncated bool                 `json:"truncated"`
}

type recentActivityInput struct {
	DaysBack *int `json:"days_back,omitempty"`
	Limit    *int `json:"limit,omitempty"`
}

type recentActivitySegment struct {
	Start     string   `json:"start"`
	End       string   `json:"end"`
	Duration  int64    `json:"duration_ns"`
	AppID     string   `json:"app_id,omitempty"`
	Domain    string   `json:"domain,omitempty"`
	Project   string   `json:"project,omitempty"`
	Topic     string   `json:"topic,omitempty"`
	SourceIDs []string `json:"source_ids"`
}

type recentActivityOutput struct {
	Segments  []recentActivitySegment `json:"segments"`
	Truncated bool                    `json:"truncated"`
}

type workingMemoryInput struct {
	Hours *int `json:"hours,omitempty"`
	Limit *int `json:"limit,omitempty"`
}

type workingMemoryItem struct {
	SourceID   string `json:"source_id"`
	AppID      string `json:"app_id"`
	Domain     string `json:"domain,omitempty"`
	CapturedAt string `json:"captured_at"`
	Content    string `json:"content"`
	Truncated  bool   `json:"truncated"`
}

type workingMemoryOutput struct {
	Start           string              `json:"start"`
	End             string              `json:"end"`
	Items           []workingMemoryItem `json:"items"`
	Truncated       bool                `json:"truncated"`
	IndexingPending bool                `json:"indexing_pending,omitempty"`
}

type wikiInput struct {
	Query string `json:"query"`
	Limit *int   `json:"limit,omitempty"`
}

type wikiSource struct {
	ID           string `json:"id,omitempty"`
	UserAuthored bool   `json:"user_authored,omitempty"`
}

type wikiFact struct {
	Text             string       `json:"text"`
	State            string       `json:"state"`
	Sources          []wikiSource `json:"sources,omitempty"`
	SourcesTruncated bool         `json:"sources_truncated,omitempty"`
}

type wikiLoop struct {
	Text             string       `json:"text"`
	State            string       `json:"state"`
	Sources          []wikiSource `json:"sources,omitempty"`
	SourcesTruncated bool         `json:"sources_truncated,omitempty"`
}

type wikiEntity struct {
	ID               string     `json:"id"`
	Kind             string     `json:"kind"`
	Name             string     `json:"name"`
	Aliases          []string   `json:"aliases,omitempty"`
	State            string     `json:"state"`
	Facts            []wikiFact `json:"facts,omitempty"`
	OpenLoops        []wikiLoop `json:"open_loops,omitempty"`
	DetailsTruncated bool       `json:"details_truncated,omitempty"`
}

type wikiOutput struct {
	Entities  []wikiEntity `json:"entities"`
	Truncated bool         `json:"truncated"`
}

type wikiPageInput struct {
	ID     string `json:"id"`
	Offset *int   `json:"offset,omitempty"`
	Limit  *int   `json:"limit,omitempty"`
}

type wikiPageEntity struct {
	ID               string   `json:"id"`
	Kind             string   `json:"kind"`
	Name             string   `json:"name"`
	Aliases          []string `json:"aliases,omitempty"`
	AliasesTruncated bool     `json:"aliases_truncated,omitempty"`
	State            string   `json:"state"`
	RelatedIDs       []string `json:"related_ids,omitempty"`
	RelationsOmitted bool     `json:"relations_omitted,omitempty"`
}

type wikiDetail struct {
	Type             string       `json:"type"`
	ID               string       `json:"id"`
	Text             string       `json:"text"`
	TextTruncated    bool         `json:"text_truncated,omitempty"`
	State            string       `json:"state"`
	Sources          []wikiSource `json:"sources"`
	SourcesTruncated bool         `json:"sources_truncated,omitempty"`
}

type wikiPageOutput struct {
	Found      bool            `json:"found"`
	Entity     *wikiPageEntity `json:"entity,omitempty"`
	Details    []wikiDetail    `json:"details"`
	Offset     int             `json:"offset"`
	NextOffset int             `json:"next_offset,omitempty"`
	HasMore    bool            `json:"has_more"`
}

func getWikiPage(ctx context.Context, reader RPCReader, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	input, err := decodeWikiPageInput(request)
	if err != nil {
		return toolError("A Library page needs a subject ID and a bounded detail offset."), nil
	}
	if reader == nil || ctx == nil || ctx.Err() != nil {
		return toolError("Blur local memory is unavailable."), nil
	}
	offset, limit := 0, 4
	if input.Offset != nil {
		offset = *input.Offset
	}
	if input.Limit != nil {
		limit = *input.Limit
	}
	params, err := json.Marshal(localapi.GetWikiPageParams{ID: input.ID, Offset: offset, Limit: limit})
	if err != nil {
		return toolError("Blur local memory is unavailable."), nil
	}
	result, err := reader.Call(ctx, localapi.RPCGetWikiPage, params)
	if err != nil || len(result) > maxSearchOutputBytes {
		clear(result)
		return toolError("Blur local memory is unavailable."), nil
	}
	defer clear(result)
	var output wikiPageOutput
	if decodeJSON(result, &output) != nil || !boundWikiPageOutput(output, input, offset, limit) {
		return toolError("Blur returned an invalid Library page."), nil
	}
	message := "No Library subject was found for that ID."
	if output.Found {
		message = fmt.Sprintf("Returned %d Library detail(s).", len(output.Details))
		if output.HasMore {
			message += " More details are available; continue at next_offset."
		}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}, StructuredContent: output}, nil
}

func decodeWikiPageInput(request *mcp.CallToolRequest) (wikiPageInput, error) {
	if request == nil || request.Params == nil || len(request.Params.Arguments) == 0 || len(request.Params.Arguments) > maxToolArgsBytes {
		return wikiPageInput{}, ErrUnavailable
	}
	var input wikiPageInput
	if decodeJSON(request.Params.Arguments, &input) != nil || !validOpaqueMemoryID(input.ID) || input.Offset != nil && (*input.Offset < 0 || *input.Offset > 256) || input.Limit != nil && (*input.Limit < 1 || *input.Limit > 4) {
		return wikiPageInput{}, ErrUnavailable
	}
	return input, nil
}

func boundWikiPageOutput(output wikiPageOutput, input wikiPageInput, offset, limit int) bool {
	if len(output.Details) > limit || len(output.Details) > 4 || output.Offset != offset || output.HasMore && (output.NextOffset != offset+len(output.Details) || output.NextOffset <= offset || output.NextOffset > 256) || !output.HasMore && output.NextOffset != 0 {
		return false
	}
	if !output.Found {
		return output.Entity == nil && len(output.Details) == 0 && !output.HasMore
	}
	if output.Entity == nil || !validOpaqueMemoryID(output.Entity.ID) || output.Entity.ID != input.ID || !validWikiKind(output.Entity.Kind) || !validOutputText(output.Entity.Name, 4096) || !validWikiState(output.Entity.State) || len(output.Entity.Aliases) > 8 || len(output.Entity.RelatedIDs) > 8 {
		return false
	}
	for _, alias := range output.Entity.Aliases {
		if !validOutputText(alias, 256) {
			return false
		}
	}
	for _, id := range output.Entity.RelatedIDs {
		if !validOpaqueMemoryID(id) || id == output.Entity.ID {
			return false
		}
	}
	for _, detail := range output.Details {
		if detail.Type != "fact" && detail.Type != "open_loop" || !validOpaqueMemoryID(detail.ID) || !validOutputText(detail.Text, 1200) || !validWikiState(detail.State) || !boundWikiSources(detail.Sources) {
			return false
		}
	}
	encoded, err := json.Marshal(output)
	return err == nil && len(encoded) <= maxSearchOutputBytes
}

func searchWiki(ctx context.Context, reader RPCReader, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	input, err := decodeWikiInput(request)
	if err != nil {
		return toolError("Wiki search needs a non-empty query and a result limit from 1 to 8."), nil
	}
	if reader == nil || ctx == nil || ctx.Err() != nil {
		return toolError("Blur local memory is unavailable."), nil
	}
	limit := defaultSearchLimit
	if input.Limit != nil {
		limit = *input.Limit
	}
	params, err := json.Marshal(localapi.SearchWikiParams{Query: strings.TrimSpace(input.Query), Limit: limit})
	if err != nil {
		return toolError("Blur local memory is unavailable."), nil
	}
	result, err := reader.Call(ctx, localapi.RPCSearchWiki, params)
	if err != nil || len(result) > maxSearchOutputBytes {
		clear(result)
		return toolError("Blur local memory is unavailable."), nil
	}
	defer clear(result)
	var output wikiOutput
	if decodeJSON(result, &output) != nil || !boundWikiOutput(output, limit) {
		return toolError("Blur returned an invalid Wiki response."), nil
	}
	message := fmt.Sprintf("Found %d Library subject(s).", len(output.Entities))
	if output.Truncated {
		message += " Some matching entries or details were omitted by the result limit."
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}, StructuredContent: output}, nil
}

func decodeWikiInput(request *mcp.CallToolRequest) (wikiInput, error) {
	if request == nil || request.Params == nil || len(request.Params.Arguments) == 0 || len(request.Params.Arguments) > maxToolArgsBytes {
		return wikiInput{}, ErrUnavailable
	}
	var input wikiInput
	if decodeJSON(request.Params.Arguments, &input) != nil || !utf8.ValidString(input.Query) || strings.IndexByte(input.Query, 0) >= 0 || strings.TrimSpace(input.Query) == "" || len(input.Query) > maxSearchQueryBytes {
		return wikiInput{}, ErrUnavailable
	}
	if input.Limit != nil && (*input.Limit < 1 || *input.Limit > maxSearchResults) {
		return wikiInput{}, ErrUnavailable
	}
	return input, nil
}

func boundWikiOutput(output wikiOutput, limit int) bool {
	if len(output.Entities) > limit || len(output.Entities) > maxSearchResults {
		return false
	}
	for _, entity := range output.Entities {
		if !validOpaqueMemoryID(entity.ID) || !validWikiKind(entity.Kind) || !validOutputText(entity.Name, 4096) || !validWikiState(entity.State) || len(entity.Aliases) > 8 || len(entity.Facts) > 6 || len(entity.OpenLoops) > 6 {
			return false
		}
		for _, alias := range entity.Aliases {
			if !validOutputText(alias, 4096) {
				return false
			}
		}
		for _, fact := range entity.Facts {
			if !validOutputText(fact.Text, 1200) || !validWikiState(fact.State) || !boundWikiSources(fact.Sources) {
				return false
			}
		}
		for _, loop := range entity.OpenLoops {
			if !validOutputText(loop.Text, 1200) || !validWikiState(loop.State) || !boundWikiSources(loop.Sources) {
				return false
			}
		}
	}
	encoded, err := json.Marshal(output)
	return err == nil && len(encoded) <= maxSearchOutputBytes
}

func boundWikiSources(sources []wikiSource) bool {
	if len(sources) > 4 {
		return false
	}
	if len(sources) == 0 {
		return false
	}
	for _, source := range sources {
		if source.UserAuthored == (source.ID != "") {
			return false
		}
		if source.ID != "" {
			if len(source.ID) != 64 || strings.ToLower(source.ID) != source.ID {
				return false
			}
			if _, err := hex.DecodeString(source.ID); err != nil {
				return false
			}
		}
	}
	return true
}

func validOpaqueMemoryID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i := range value {
		c := value[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func validWikiKind(value string) bool {
	switch value {
	case "person", "project", "topic", "place", "workflow":
		return true
	default:
		return false
	}
}

func validWikiState(value string) bool {
	switch value {
	case "confirmed", "deterministic", "proposed", "dismissed", "merged":
		return true
	default:
		return false
	}
}

type taskInput struct {
	State string `json:"state,omitempty"`
	Limit *int   `json:"limit,omitempty"`
	After string `json:"after,omitempty"`
}

type taskReminder struct {
	DueAt   string `json:"due_at"`
	Repeat  string `json:"repeat,omitempty"`
	Hour    int    `json:"hour,omitempty"`
	Minute  int    `json:"minute,omitempty"`
	Weekday int    `json:"weekday,omitempty"`
}

type taskTransition struct {
	State    string `json:"state"`
	At       string `json:"at"`
	Revision uint64 `json:"revision"`
}

type taskItem struct {
	ID               string           `json:"id"`
	Revision         uint64           `json:"revision"`
	Title            string           `json:"title"`
	State            string           `json:"state"`
	CreatedAt        string           `json:"created_at"`
	UpdatedAt        string           `json:"updated_at"`
	SourceIDs        []string         `json:"source_ids,omitempty"`
	UserAuthored     bool             `json:"user_authored,omitempty"`
	DeliveryState    string           `json:"delivery_state,omitempty"`
	Reminder         *taskReminder    `json:"reminder,omitempty"`
	History          []taskTransition `json:"history,omitempty"`
	HistoryOmitted   uint64           `json:"history_omitted,omitempty"`
	HistoryTruncated bool             `json:"history_truncated,omitempty"`
}

type taskOutput struct {
	Items   []taskItem `json:"items"`
	Next    string     `json:"next,omitempty"`
	HasMore bool       `json:"has_more"`
}

func listTasks(ctx context.Context, reader RPCReader, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	input, err := decodeTaskInput(request)
	if err != nil {
		return toolError("Task list accepts open, completed, cancelled or all, with up to 8 results."), nil
	}
	if reader == nil || ctx == nil || ctx.Err() != nil {
		return toolError("Blur local memory is unavailable."), nil
	}
	limit := defaultSearchLimit
	if input.Limit != nil {
		limit = *input.Limit
	}
	state := input.State
	if state == "" {
		state = "open"
	}
	params, err := json.Marshal(localapi.ListTasksParams{State: state, Limit: limit, After: input.After})
	if err != nil {
		return toolError("Blur local memory is unavailable."), nil
	}
	result, err := reader.Call(ctx, localapi.RPCListTasks, params)
	if err != nil || len(result) > maxSearchOutputBytes {
		clear(result)
		return toolError("Blur local memory is unavailable."), nil
	}
	defer clear(result)
	var output taskOutput
	if decodeJSON(result, &output) != nil || !boundTaskOutput(output, limit) {
		return toolError("Blur returned an invalid task list."), nil
	}
	message := fmt.Sprintf("Returned %d task(s).", len(output.Items))
	if output.HasMore {
		message += " More tasks are available on the next page."
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}, StructuredContent: output}, nil
}

func decodeTaskInput(request *mcp.CallToolRequest) (taskInput, error) {
	if request == nil || request.Params == nil || len(request.Params.Arguments) > maxToolArgsBytes {
		return taskInput{}, ErrUnavailable
	}
	var input taskInput
	if len(request.Params.Arguments) > 0 && decodeJSON(request.Params.Arguments, &input) != nil {
		return taskInput{}, ErrUnavailable
	}
	if input.State != "" && input.State != "open" && input.State != "completed" && input.State != "cancelled" && input.State != "all" || input.Limit != nil && (*input.Limit < 1 || *input.Limit > 8) || input.After != "" && !validTaskID(input.After) {
		return taskInput{}, ErrUnavailable
	}
	return input, nil
}

func boundTaskOutput(output taskOutput, limit int) bool {
	if len(output.Items) > limit || len(output.Items) > 8 || output.HasMore && !validTaskID(output.Next) || !output.HasMore && output.Next != "" {
		return false
	}
	for _, item := range output.Items {
		created, createdErr := time.Parse(time.RFC3339Nano, item.CreatedAt)
		updated, updatedErr := time.Parse(time.RFC3339Nano, item.UpdatedAt)
		if !validTaskID(item.ID) || item.Revision == 0 || !validOutputText(item.Title, 512) || !validTaskState(item.State) || createdErr != nil || updatedErr != nil || updated.Before(created) || len(item.SourceIDs) > 8 || len(item.History) > 4 || item.HistoryOmitted > 1<<48 {
			return false
		}
		if item.UserAuthored == (len(item.SourceIDs) > 0) {
			return false
		}
		seen := map[string]bool{}
		for _, sourceID := range item.SourceIDs {
			if len(sourceID) != 64 || strings.ToLower(sourceID) != sourceID || seen[sourceID] {
				return false
			}
			if _, err := hex.DecodeString(sourceID); err != nil {
				return false
			}
			seen[sourceID] = true
		}
		if item.Reminder != nil {
			due, err := time.Parse(time.RFC3339Nano, item.Reminder.DueAt)
			if err != nil || due.Year() < 2020 || due.Year() > 2100 || item.Reminder.Repeat != "" && item.Reminder.Repeat != "daily" && item.Reminder.Repeat != "weekly" || item.Reminder.Hour < 0 || item.Reminder.Hour > 23 || item.Reminder.Minute < 0 || item.Reminder.Minute > 59 || item.Reminder.Weekday < 0 || item.Reminder.Weekday > 6 || item.DeliveryState == "" {
				return false
			}
		} else if item.DeliveryState != "" {
			return false
		}
		if item.DeliveryState != "" && item.DeliveryState != "needs_sync" && item.DeliveryState != "scheduling" && item.DeliveryState != "scheduled" && item.DeliveryState != "delivered" && item.DeliveryState != "elapsed" && item.DeliveryState != "blocked" && item.DeliveryState != "failed" {
			return false
		}
		for _, transition := range item.History {
			if !validTaskState(transition.State) || transition.Revision == 0 || transition.Revision > item.Revision {
				return false
			}
			at, err := time.Parse(time.RFC3339Nano, transition.At)
			if err != nil || at.Before(created) || at.After(updated) {
				return false
			}
		}
	}
	encoded, err := json.Marshal(output)
	return err == nil && len(encoded) <= maxSearchOutputBytes
}

func validTaskID(value string) bool {
	if len(value) != 32 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validTaskState(value string) bool {
	return value == "open" || value == "completed" || value == "cancelled"
}

func getRecentActivity(ctx context.Context, reader RPCReader, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	input, err := decodeRecentActivityInput(request)
	if err != nil {
		return toolError("Recent activity accepts 1 to 7 days and up to 8 results."), nil
	}
	if reader == nil || ctx == nil || ctx.Err() != nil {
		return toolError("Blur local memory is unavailable."), nil
	}
	days, limit := 1, 5
	if input.DaysBack != nil {
		days = *input.DaysBack
	}
	if input.Limit != nil {
		limit = *input.Limit
	}
	params, err := json.Marshal(localapi.GetRecentActivityParams{DaysBack: days, Limit: limit})
	if err != nil {
		return toolError("Blur local memory is unavailable."), nil
	}
	result, err := reader.Call(ctx, localapi.RPCGetRecentActivity, params)
	if err != nil || len(result) > localapi.MaxRPCSnapshotResponseBytes {
		clear(result)
		return toolError("Blur local memory is unavailable."), nil
	}
	defer clear(result)
	var output recentActivityOutput
	if decodeJSON(result, &output) != nil || !boundRecentActivityOutput(output, limit) {
		return toolError("Blur returned an invalid recent activity response."), nil
	}
	message := fmt.Sprintf("Returned %d recent activity block(s).", len(output.Segments))
	if output.Truncated {
		message += " More activity was omitted by the result limit."
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}, StructuredContent: output}, nil
}

func getWorkingMemory(ctx context.Context, reader RPCReader, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	input, err := decodeWorkingMemoryInput(request)
	if err != nil {
		return toolError("Recent context needs a lookback from 1 to 6 hours and a result limit from 1 to 8."), nil
	}
	if reader == nil || ctx == nil || ctx.Err() != nil {
		return toolError("Blur local memory is unavailable."), nil
	}
	params, err := json.Marshal(localapi.GetWorkingMemoryParams{Hours: *input.Hours, Limit: *input.Limit})
	if err != nil {
		return toolError("Blur local memory is unavailable."), nil
	}
	result, err := reader.Call(ctx, localapi.RPCGetWorkingMemory, params)
	if err != nil || len(result) > maxSearchOutputBytes {
		clear(result)
		return toolError("Blur local memory is unavailable."), nil
	}
	defer clear(result)
	var output workingMemoryOutput
	if decodeJSON(result, &output) != nil || !boundWorkingMemoryOutput(output, input) {
		return toolError("Blur returned invalid recent context."), nil
	}
	message := "No saved captures in that recent time window."
	if len(output.Items) > 0 {
		message = "Returned recent capture excerpts with source IDs. Open the cited captures before making exact claims."
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}, StructuredContent: output}, nil
}

func decodeWorkingMemoryInput(request *mcp.CallToolRequest) (workingMemoryInput, error) {
	if request == nil || request.Params == nil || len(request.Params.Arguments) > maxToolArgsBytes {
		return workingMemoryInput{}, ErrUnavailable
	}
	var input workingMemoryInput
	if len(request.Params.Arguments) > 0 && decodeJSON(request.Params.Arguments, &input) != nil {
		return workingMemoryInput{}, ErrUnavailable
	}
	if input.Hours == nil {
		value := 4
		input.Hours = &value
	}
	if input.Limit == nil {
		value := 5
		input.Limit = &value
	}
	if *input.Hours < 1 || *input.Hours > 6 || *input.Limit < 1 || *input.Limit > 8 {
		return workingMemoryInput{}, ErrUnavailable
	}
	return input, nil
}

func boundWorkingMemoryOutput(output workingMemoryOutput, input workingMemoryInput) bool {
	if input.Hours == nil || input.Limit == nil || len(output.Items) > *input.Limit {
		return false
	}
	start, err := time.Parse(time.RFC3339Nano, output.Start)
	if err != nil {
		return false
	}
	end, err := time.Parse(time.RFC3339Nano, output.End)
	if err != nil || !end.After(start) || end.Sub(start) != time.Duration(*input.Hours)*time.Hour+time.Nanosecond {
		return false
	}
	seen := make(map[string]bool, len(output.Items))
	previous := end
	for _, item := range output.Items {
		if len(item.SourceID) != 64 || strings.ToLower(item.SourceID) != item.SourceID || seen[item.SourceID] || !validOutputText(item.AppID, 128) || strings.EqualFold(item.AppID, appidentity.BundleID) || item.Domain != "" && !validOutputText(item.Domain, 253) || !validOutputText(item.Content, 1600) {
			return false
		}
		if _, err := hex.DecodeString(item.SourceID); err != nil {
			return false
		}
		seen[item.SourceID] = true
		at, err := time.Parse(time.RFC3339Nano, item.CapturedAt)
		if err != nil || at.Before(start) || !at.Before(end) || at.After(previous) {
			return false
		}
		previous = at
		if item.Truncated && !output.Truncated {
			return false
		}
	}
	encoded, err := json.Marshal(output)
	return err == nil && len(encoded) <= maxSearchOutputBytes
}

func decodeRecentActivityInput(request *mcp.CallToolRequest) (recentActivityInput, error) {
	if request == nil || request.Params == nil || len(request.Params.Arguments) > maxToolArgsBytes {
		return recentActivityInput{}, ErrUnavailable
	}
	var input recentActivityInput
	if len(request.Params.Arguments) > 0 && decodeJSON(request.Params.Arguments, &input) != nil {
		return recentActivityInput{}, ErrUnavailable
	}
	if input.DaysBack != nil && (*input.DaysBack < 1 || *input.DaysBack > 7) || input.Limit != nil && (*input.Limit < 1 || *input.Limit > 8) {
		return recentActivityInput{}, ErrUnavailable
	}
	return input, nil
}

func boundRecentActivityOutput(output recentActivityOutput, limit int) bool {
	if len(output.Segments) > limit {
		return false
	}
	for _, segment := range output.Segments {
		start, err := time.Parse(time.RFC3339Nano, segment.Start)
		if err != nil {
			return false
		}
		end, err := time.Parse(time.RFC3339Nano, segment.End)
		if err != nil || !end.After(start) || segment.Duration != end.Sub(start).Nanoseconds() || len(segment.SourceIDs) > 32 || segment.AppID != "" && !validOutputText(segment.AppID, 128) || segment.Domain != "" && !validOutputText(segment.Domain, 253) || segment.Project != "" && !validOutputText(segment.Project, 128) || segment.Topic != "" && !validOutputText(segment.Topic, 128) {
			return false
		}
		seen := map[string]bool{}
		for _, id := range segment.SourceIDs {
			if len(id) != 64 || strings.ToLower(id) != id || seen[id] {
				return false
			}
			if _, err := hex.DecodeString(id); err != nil {
				return false
			}
			seen[id] = true
		}
	}
	encoded, err := json.Marshal(output)
	return err == nil && len(encoded) <= localapi.MaxRPCSnapshotResponseBytes
}

func getChronicle(ctx context.Context, reader RPCReader, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	input, err := decodeChronicleInput(request)
	if err != nil {
		return toolError("A chronicle needs level hour with an hour ID like hour:YYYYMMDDTHHZ, level day with a period like YYYY-MM-DD, or level week with a period like YYYY-Www."), nil
	}
	if reader == nil || ctx == nil || ctx.Err() != nil {
		return toolError("Blur local memory is unavailable."), nil
	}
	params, err := json.Marshal(localapi.GetChronicleParams{HourID: input.HourID, Level: input.Level, Period: input.Period})
	if err != nil {
		return toolError("Blur local memory is unavailable."), nil
	}
	result, err := reader.Call(ctx, localapi.RPCGetChronicle, params)
	if err != nil || len(result) > maxSearchOutputBytes {
		clear(result)
		return toolError("Blur local memory is unavailable."), nil
	}
	defer clear(result)
	var output chronicleOutput
	if decodeJSON(result, &output) != nil || !boundChronicleOutput(output, input) {
		return toolError("Blur returned an invalid chronicle response."), nil
	}
	message := "No saved hourly recap for " + input.HourID + "."
	if input.Level != "" {
		message = "No saved " + input.Level + " recap for " + input.Period + "."
	}
	if output.Found {
		message = "Returned the hourly recap and its source IDs. Open the cited captures before making exact claims."
		if input.Level != "" {
			message = "Returned the " + input.Level + " recap and its source IDs. It summarizes hourly recaps; open the cited captures before making exact claims."
		}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}, StructuredContent: output}, nil
}

func getChronicles(ctx context.Context, reader RPCReader, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	input, start, end, err := decodeChronicleRangeInput(request)
	if err != nil {
		return toolError("A recap range needs RFC3339 start and end times, a range no longer than 32 days, and a valid optional hour cursor."), nil
	}
	if reader == nil || ctx == nil || ctx.Err() != nil {
		return toolError("Blur local memory is unavailable."), nil
	}
	params, err := json.Marshal(localapi.ListChroniclesParams{Start: start, End: end, After: input.After, Limit: *input.Limit})
	if err != nil {
		return toolError("Blur local memory is unavailable."), nil
	}
	result, err := reader.Call(ctx, localapi.RPCListChronicles, params)
	if err != nil || len(result) > localapi.MaxRPCSearchResponseBytes {
		clear(result)
		return toolError("Blur local memory is unavailable."), nil
	}
	defer clear(result)
	var output chronicleRangeOutput
	if decodeJSON(result, &output) != nil || !boundChronicleRangeOutput(output, input, start, end) {
		return toolError("Blur returned an invalid recap range."), nil
	}
	message := "No saved hourly recaps in that range."
	if len(output.Items) > 0 {
		message = "Returned source-linked hourly recaps. Use their capture source IDs to verify exact details."
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}, StructuredContent: output}, nil
}

func decodeChronicleRangeInput(request *mcp.CallToolRequest) (chronicleRangeInput, time.Time, time.Time, error) {
	if request == nil || request.Params == nil || len(request.Params.Arguments) == 0 || len(request.Params.Arguments) > maxToolArgsBytes {
		return chronicleRangeInput{}, time.Time{}, time.Time{}, ErrUnavailable
	}
	var input chronicleRangeInput
	if decodeJSON(request.Params.Arguments, &input) != nil {
		return chronicleRangeInput{}, time.Time{}, time.Time{}, ErrUnavailable
	}
	start, startErr := time.Parse(time.RFC3339Nano, input.Start)
	end, endErr := time.Parse(time.RFC3339Nano, input.End)
	if startErr != nil || endErr != nil || !start.Before(end) || end.Sub(start) > 32*24*time.Hour || input.After != "" && !validChronicleHourID(input.After) {
		return chronicleRangeInput{}, time.Time{}, time.Time{}, ErrUnavailable
	}
	if input.Limit == nil {
		limit := 8
		input.Limit = &limit
	}
	if *input.Limit < 1 || *input.Limit > 8 {
		return chronicleRangeInput{}, time.Time{}, time.Time{}, ErrUnavailable
	}
	return input, start.UTC(), end.UTC(), nil
}

func validChronicleHourID(id string) bool {
	const prefix = "hour:"
	if len(id) != len(prefix)+len("20060102T15Z") || !strings.HasPrefix(id, prefix) {
		return false
	}
	_, err := time.Parse("20060102T15Z", id[len(prefix):])
	return err == nil
}

func boundChronicleRangeOutput(output chronicleRangeOutput, input chronicleRangeInput, start, end time.Time) bool {
	if input.Limit == nil || len(output.Items) > *input.Limit || output.Truncated != (output.Next != "") || output.Next != "" && !validChronicleHourID(output.Next) {
		return false
	}
	previous := input.After
	for _, item := range output.Items {
		if !validChronicleHourID(item.HourID) || item.HourID <= previous || !validOutputText(item.Summary, 4096) || len(item.SourceIDs) == 0 || len(item.SourceIDs) > 64 {
			return false
		}
		itemStart, err := time.Parse(time.RFC3339Nano, item.Start)
		if err != nil || itemStart.Before(start) || !itemStart.Before(end) {
			return false
		}
		itemEnd, err := time.Parse(time.RFC3339Nano, item.End)
		if err != nil || !itemEnd.Equal(itemStart.Add(time.Hour)) || "hour:"+itemStart.UTC().Format("20060102T15Z") != item.HourID {
			return false
		}
		previous = item.HourID
		last := ""
		for _, id := range item.SourceIDs {
			if len(id) != 64 || strings.ToLower(id) != id || id <= last {
				return false
			}
			if _, err := hex.DecodeString(id); err != nil {
				return false
			}
			last = id
		}
		if followUp := item.FollowUp; followUp != nil {
			found := false
			for _, id := range item.SourceIDs {
				found = found || id == followUp.SourceID
			}
			if !found || !validOutputText(followUp.SourceID, 64) || len(followUp.SourceID) != 64 || !validOutputText(followUp.Text, 512) {
				return false
			}
		}
	}
	if len(output.Items) == 0 && output.Next != "" || output.Next != "" && output.Next != previous {
		return false
	}
	encoded, err := json.Marshal(output)
	return err == nil && len(encoded) <= localapi.MaxRPCSearchResponseBytes
}

func decodeChronicleInput(request *mcp.CallToolRequest) (chronicleInput, error) {
	if request == nil || request.Params == nil || len(request.Params.Arguments) == 0 || len(request.Params.Arguments) > maxToolArgsBytes {
		return chronicleInput{}, ErrUnavailable
	}
	var input chronicleInput
	if decodeJSON(request.Params.Arguments, &input) != nil {
		return chronicleInput{}, ErrUnavailable
	}
	switch input.Level {
	case "", "hour":
		if len(input.HourID) != len("hour:20060102T15Z") || !strings.HasPrefix(input.HourID, "hour:") || input.Period != "" {
			return chronicleInput{}, ErrUnavailable
		}
		if _, err := time.Parse("20060102T15Z", input.HourID[len("hour:"):]); err != nil {
			return chronicleInput{}, ErrUnavailable
		}
		input.Level = ""
	case "day":
		if input.HourID != "" || !validDayPeriod(input.Period) {
			return chronicleInput{}, ErrUnavailable
		}
	case "week":
		if input.HourID != "" || !validWeekPeriod(input.Period) {
			return chronicleInput{}, ErrUnavailable
		}
	default:
		return chronicleInput{}, ErrUnavailable
	}
	return input, nil
}

func validDayPeriod(period string) bool {
	if len(period) != len("2006-01-02") {
		return false
	}
	parsed, err := time.Parse("2006-01-02", period)
	return err == nil && parsed.Format("2006-01-02") == period
}

// validWeekPeriod accepts only a real ISO week such as 2026-W46.
func validWeekPeriod(period string) bool {
	if len(period) != len("2006-W01") || period[4:6] != "-W" {
		return false
	}
	year, err := strconv.Atoi(period[:4])
	week, werr := strconv.Atoi(period[6:])
	if err != nil || werr != nil || year < 1 || week < 1 || week > 53 {
		return false
	}
	// January 4th always falls in ISO week one; step to the requested week.
	jan4 := time.Date(year, time.January, 4, 0, 0, 0, 0, time.UTC)
	monday := jan4.AddDate(0, 0, -((int(jan4.Weekday())+6)%7)+(week-1)*7)
	y, w := monday.ISOWeek()
	return y == year && w == week
}

func boundChronicleOutput(output chronicleOutput, request chronicleInput) bool {
	if request.Level != "" {
		return boundRollupOutput(output, request)
	}
	if output.Level != "" || output.Period != "" || output.Text != "" || len(output.ChildIDs) != 0 || output.SourceIDsTruncated || output.GeneratedAt != "" || output.Dirty {
		return false
	}
	if !output.Found {
		return output.HourID == "" && output.Start == "" && output.End == "" && output.Summary == "" && len(output.SourceIDs) == 0 && output.FollowUp == nil
	}
	if output.HourID != request.HourID || !validOutputText(output.Summary, 4096) || len(output.SourceIDs) == 0 || len(output.SourceIDs) > 64 {
		return false
	}
	start, err := time.Parse(time.RFC3339Nano, output.Start)
	if err != nil {
		return false
	}
	end, err := time.Parse(time.RFC3339Nano, output.End)
	if err != nil || !end.Equal(start.Add(time.Hour)) || "hour:"+start.UTC().Format("20060102T15Z") != request.HourID {
		return false
	}
	last := ""
	for _, id := range output.SourceIDs {
		if len(id) != 64 || strings.ToLower(id) != id || id <= last {
			return false
		}
		if _, err := hex.DecodeString(id); err != nil {
			return false
		}
		last = id
	}
	if output.FollowUp != nil {
		if !validOutputText(output.FollowUp.SourceID, 64) || !validOutputText(output.FollowUp.Text, 512) {
			return false
		}
		found := false
		for _, id := range output.SourceIDs {
			found = found || id == output.FollowUp.SourceID
		}
		if !found {
			return false
		}
	}
	encoded, err := json.Marshal(output)
	return err == nil && len(encoded) <= maxSearchOutputBytes
}

type snapshotInput struct {
	SourceIDs         []string       `json:"source_ids"`
	Offsets           map[string]int `json:"offsets,omitempty"`
	MaxBytesPerSource int            `json:"max_bytes_per_source,omitempty"`
}

type snapshotItem struct {
	SourceID           string `json:"source_id"`
	AppID              string `json:"app_id"`
	Domain             string `json:"domain,omitempty"`
	CapturedAt         string `json:"captured_at,omitempty"`
	Content            string `json:"content"`
	OffsetBytes        int    `json:"offset_bytes"`
	NextOffsetBytes    int    `json:"next_offset_bytes"`
	Complete           bool   `json:"complete"`
	PartialCapture     bool   `json:"partial_capture,omitempty"`
	DocumentID         string `json:"document_id,omitempty"`
	PartIndex          *int64 `json:"part_index,omitempty"`
	PartCount          *int64 `json:"part_count,omitempty"`
	DocumentOffset     *int64 `json:"document_offset_bytes,omitempty"`
	DocumentTotalBytes *int64 `json:"document_total_bytes,omitempty"`
}

type snapshotOutput struct {
	Snapshots []snapshotItem `json:"snapshots"`
	Truncated bool           `json:"truncated"`
}

func getSnapshots(ctx context.Context, reader RPCReader, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	input, err := decodeSnapshotInput(request)
	if err != nil {
		return toolError("Snapshots need one to three valid source IDs, optional non-negative offsets, and a chunk size from 4 to 4096 bytes."), nil
	}
	if reader == nil || ctx == nil || ctx.Err() != nil {
		return toolError("Blur local memory is unavailable."), nil
	}
	if input.MaxBytesPerSource == 0 {
		input.MaxBytesPerSource = 4096
	}
	params, err := json.Marshal(localapi.GetSnapshotsParams{SourceIDs: input.SourceIDs, Offsets: input.Offsets, MaxBytesPerSource: input.MaxBytesPerSource})
	if err != nil {
		return toolError("Blur local memory is unavailable."), nil
	}
	result, err := reader.Call(ctx, localapi.RPCGetSnapshots, params)
	if err != nil || len(result) > localapi.MaxRPCSnapshotResponseBytes {
		clear(result)
		return toolError("Blur local memory is unavailable."), nil
	}
	defer clear(result)
	var output snapshotOutput
	if decodeJSON(result, &output) != nil {
		return toolError("Blur returned an invalid snapshot response."), nil
	}
	output, ok := boundSnapshotOutput(output, input)
	if !ok {
		return toolError("Blur returned an invalid snapshot response."), nil
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Returned %d saved capture chunk(s).", len(output.Snapshots))}},
		StructuredContent: output,
	}, nil
}

func decodeSnapshotInput(request *mcp.CallToolRequest) (snapshotInput, error) {
	if request == nil || request.Params == nil || len(request.Params.Arguments) == 0 {
		return snapshotInput{}, ErrUnavailable
	}
	encoded, err := json.Marshal(request.Params.Arguments)
	if err != nil || len(encoded) > 2<<10 {
		return snapshotInput{}, ErrUnavailable
	}
	defer clear(encoded)
	var input snapshotInput
	if decodeJSON(encoded, &input) != nil || len(input.SourceIDs) < 1 || len(input.SourceIDs) > 3 || input.MaxBytesPerSource != 0 && (input.MaxBytesPerSource < 4 || input.MaxBytesPerSource > 4096) || len(input.Offsets) > len(input.SourceIDs) {
		return snapshotInput{}, ErrUnavailable
	}
	seen := make(map[string]bool, len(input.SourceIDs))
	for _, id := range input.SourceIDs {
		if len(id) != 64 || strings.ToLower(id) != id || !validOutputText(id, 64) {
			return snapshotInput{}, ErrUnavailable
		}
		if _, err := hex.DecodeString(id); err != nil || seen[id] {
			return snapshotInput{}, ErrUnavailable
		}
		seen[id] = true
	}
	for id, offset := range input.Offsets {
		if !seen[id] || offset < 0 || offset > 1<<20 {
			return snapshotInput{}, ErrUnavailable
		}
	}
	return input, nil
}

func boundSnapshotOutput(input snapshotOutput, request snapshotInput) (snapshotOutput, bool) {
	if len(input.Snapshots) > len(request.SourceIDs) {
		return snapshotOutput{}, false
	}
	allowed := make(map[string]bool, len(request.SourceIDs))
	for _, id := range request.SourceIDs {
		allowed[id] = true
	}
	seen := map[string]bool{}
	used := 0
	for _, item := range input.Snapshots {
		_, sourceIDErr := hex.DecodeString(item.SourceID)
		if sourceIDErr != nil || !allowed[item.SourceID] || seen[item.SourceID] || !validOutputText(item.AppID, 128) || item.Domain != "" && !validOutputText(item.Domain, 253) || !utf8.ValidString(item.Content) || strings.IndexByte(item.Content, 0) >= 0 || len(item.Content) > request.MaxBytesPerSource || len(item.Content) > 4096 || item.OffsetBytes != request.Offsets[item.SourceID] || item.NextOffsetBytes != item.OffsetBytes+len(item.Content) || item.NextOffsetBytes < item.OffsetBytes || item.CapturedAt != "" && len(item.CapturedAt) > 40 {
			return snapshotOutput{}, false
		}
		if item.CapturedAt != "" {
			if _, err := time.Parse(time.RFC3339Nano, item.CapturedAt); err != nil {
				return snapshotOutput{}, false
			}
		}
		for _, value := range []*int64{item.PartIndex, item.PartCount, item.DocumentOffset, item.DocumentTotalBytes} {
			if value != nil && *value < 0 {
				return snapshotOutput{}, false
			}
		}
		if item.PartCount != nil && *item.PartCount < 1 || item.PartIndex != nil && item.PartCount != nil && *item.PartIndex >= *item.PartCount {
			return snapshotOutput{}, false
		}
		seen[item.SourceID] = true
		used += len(item.SourceID) + len(item.AppID) + len(item.Domain) + len(item.CapturedAt) + len(item.Content) + 192
		if used > localapi.MaxRPCSnapshotResponseBytes {
			return snapshotOutput{}, false
		}
	}
	out := snapshotOutput{Snapshots: input.Snapshots, Truncated: input.Truncated}
	return out, true
}

// RunStdio runs the Claude-launched, newline-delimited MCP process. The SDK's
// default 16 MiB frame allowance is reduced to 64 KiB for this bounded tool.
func RunStdio(ctx context.Context, reader RPCReader) error {
	if ctx == nil || reader == nil {
		return ErrUnavailable
	}
	return New(reader).Run(ctx, &mcp.StdioTransport{MaxLineLength: maxStdioLineBytes})
}

func searchMemory(ctx context.Context, reader RPCReader, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	input, err := decodeSearchInput(request)
	if err != nil {
		return toolError("Search needs a non-empty query of at most 512 bytes and a limit from 1 to 8."), nil
	}
	if reader == nil || ctx == nil || ctx.Err() != nil {
		return toolError("Blur local memory is unavailable."), nil
	}
	limit := defaultSearchLimit
	if input.Limit != nil {
		limit = *input.Limit
	}
	query := strings.TrimSpace(input.Query)
	params, err := json.Marshal(localapi.SearchMemoryParams{Query: query, Limit: limit})
	if err != nil {
		return toolError("Blur local memory is unavailable."), nil
	}
	result, err := reader.Call(ctx, localapi.RPCSearchMemory, params)
	if err != nil || len(result) > localapi.MaxRPCSearchResponseBytes {
		clear(result)
		return toolError("Blur local memory is unavailable."), nil
	}
	defer clear(result)
	var output searchMemoryOutput
	if decodeJSON(result, &output) != nil {
		return toolError("Blur returned an invalid memory response."), nil
	}
	output, ok := boundSearchOutput(output, limit)
	if !ok {
		return toolError("Blur returned an invalid memory response."), nil
	}
	count := len(output.Results)
	summary := searchSummary(count, output.Truncated)
	if output.IndexingPending {
		summary += " Some saved memory is still being indexed."
	}
	if output.CoverageLimited {
		summary += " Results may omit matches by meaning or older task changes."
	}
	for _, hit := range output.Results {
		if hit.TimeUnknown {
			summary += " Some original capture times are unknown; do not infer when that content was viewed."
			break
		}
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: summary}},
		StructuredContent: output,
	}, nil
}

func decodeSearchInput(request *mcp.CallToolRequest) (searchMemoryInput, error) {
	if request == nil || request.Params == nil || len(request.Params.Arguments) == 0 || len(request.Params.Arguments) > maxToolArgsBytes {
		return searchMemoryInput{}, ErrUnavailable
	}
	var input searchMemoryInput
	if decodeJSON(request.Params.Arguments, &input) != nil || !utf8.ValidString(input.Query) || strings.IndexByte(input.Query, 0) >= 0 || len(input.Query) == 0 || len(input.Query) > maxSearchQueryBytes || strings.TrimSpace(input.Query) == "" {
		return searchMemoryInput{}, ErrUnavailable
	}
	if input.Limit != nil && (*input.Limit < 1 || *input.Limit > maxSearchResults) {
		return searchMemoryInput{}, ErrUnavailable
	}
	return input, nil
}

func decodeJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return ErrUnavailable
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return ErrUnavailable
	}
	return nil
}

func boundSearchOutput(input searchMemoryOutput, limit int) (searchMemoryOutput, bool) {
	output := searchMemoryOutput{Results: make([]searchMemoryHit, 0, min(len(input.Results), limit)), Truncated: input.Truncated, IndexingPending: input.IndexingPending, CoverageLimited: input.CoverageLimited}
	used := 0
	for _, hit := range input.Results {
		if len(output.Results) >= limit {
			output.Truncated = true
			break
		}
		if !validOutputText(hit.SourceID, 64) || !validOutputText(hit.AppID, 128) || (hit.Domain != "" && !validOutputText(hit.Domain, 253)) || !utf8.ValidString(hit.Text) || strings.IndexByte(hit.Text, 0) >= 0 {
			output.Truncated = true
			continue
		}
		if hit.TimeUnknown {
			hit.CapturedAt = ""
		}
		if hit.CapturedAt != "" {
			if _, err := time.Parse(time.RFC3339Nano, hit.CapturedAt); err != nil || len(hit.CapturedAt) > 40 {
				output.Truncated = true
				continue
			}
		}
		if len(hit.Text) > maxSearchTextBytes {
			hit.Text = hit.Text[:validUTF8Cut(hit.Text, maxSearchTextBytes)]
			hit.Truncated = true
		}
		cost := len(hit.SourceID) + len(hit.AppID) + len(hit.Domain) + len(hit.Text) + len(hit.CapturedAt)
		if cost > maxSearchOutputBytes-used {
			output.Truncated = true
			break
		}
		used += cost
		output.Truncated = output.Truncated || hit.Truncated || hit.Partial
		output.Results = append(output.Results, hit)
	}
	encoded, err := json.Marshal(output)
	if err != nil || len(encoded) > maxSearchOutputBytes {
		return searchMemoryOutput{}, false
	}
	return output, true
}

func validOutputText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && strings.IndexByte(value, 0) < 0
}

func validUTF8Cut(value string, maximum int) int {
	cut := maximum
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return cut
}

func searchSummary(count int, truncated bool) string {
	if truncated {
		return "Found " + strconv.Itoa(count) + " local matches; results were bounded."
	}
	return "Found " + strconv.Itoa(count) + " local matches."
}

func toolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}, IsError: true}
}

// boundRollupOutput validates a day or week recap response against its request.
// Derived text is accepted only with the capture source IDs that support it.
func boundRollupOutput(output chronicleOutput, request chronicleInput) bool {
	if output.HourID != "" || output.Summary != "" || output.FollowUp != nil {
		return false
	}
	if !output.Found {
		return output.Level == "" && output.Period == "" && output.Start == "" && output.End == "" && output.Text == "" && len(output.ChildIDs) == 0 && len(output.SourceIDs) == 0 && !output.SourceIDsTruncated && output.GeneratedAt == "" && !output.Dirty
	}
	if output.Level != request.Level || output.Period != request.Period || !validOutputText(output.Text, 2048) || len(output.SourceIDs) == 0 || len(output.SourceIDs) > 64 || len(output.ChildIDs) == 0 || len(output.ChildIDs) > 64 {
		return false
	}
	start, err := time.Parse(time.RFC3339Nano, output.Start)
	if err != nil {
		return false
	}
	end, err := time.Parse(time.RFC3339Nano, output.End)
	if err != nil || !end.After(start) || end.Sub(start) > 8*24*time.Hour {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, output.GeneratedAt); err != nil {
		return false
	}
	last := ""
	for _, id := range output.SourceIDs {
		if len(id) != 64 || strings.ToLower(id) != id || id <= last {
			return false
		}
		if _, err := hex.DecodeString(id); err != nil {
			return false
		}
		last = id
	}
	last = ""
	for _, id := range output.ChildIDs {
		if !validOutputText(id, 18) || id <= last {
			return false
		}
		last = id
	}
	encoded, err := json.Marshal(output)
	return err == nil && len(encoded) <= maxSearchOutputBytes
}
