// Package mcpserver exposes a deliberately small, read-only MCP surface over
// Blur's authenticated local API. It never opens the encrypted database.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

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

var ErrUnavailable = errors.New("Blur local memory unavailable")

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
	Results   []searchMemoryHit `json:"results"`
	Truncated bool              `json:"truncated"`
}

type searchMemoryHit struct {
	SourceID  string `json:"source_id"`
	AppID     string `json:"app_id"`
	Domain    string `json:"domain,omitempty"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

const searchInputSchema = `{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string","minLength":1,"maxLength":512,"description":"Words to match against locally saved Blur memory."},"limit":{"type":"integer","minimum":1,"maximum":8,"description":"Maximum results to return; defaults to 5."}},"required":["query"]}`

const searchOutputSchema = `{"type":"object","additionalProperties":false,"properties":{"results":{"type":"array","maxItems":8,"items":{"type":"object","additionalProperties":false,"properties":{"source_id":{"type":"string","maxLength":64},"app_id":{"type":"string","maxLength":128},"domain":{"type":"string","maxLength":253},"text":{"type":"string","maxLength":2048},"truncated":{"type":"boolean"}},"required":["source_id","app_id","text"]}},"truncated":{"type":"boolean"}},"required":["results","truncated"]}`

// New creates a server with one bounded read-only tool and no resources or
// prompts. Tool results are explicitly returned to the MCP host, which may
// forward them to its configured model provider.
func New(reader RPCReader) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "blur", Title: "Blur local memory", Version: "0.1.0"}, nil)
	openWorld := false
	server.AddTool(&mcp.Tool{
		Name:         "search_memory",
		Title:        "Search Blur memory",
		Description:  "Search locally stored Blur memory using exact text matching. Results are returned to the MCP host and may be sent to its configured model provider.",
		InputSchema:  json.RawMessage(searchInputSchema),
		OutputSchema: json.RawMessage(searchOutputSchema),
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &openWorld},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return searchMemory(ctx, reader, request)
	})
	return server
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
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: searchSummary(count, output.Truncated)}},
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
	output := searchMemoryOutput{Results: make([]searchMemoryHit, 0, min(len(input.Results), limit)), Truncated: input.Truncated}
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
		if len(hit.Text) > maxSearchTextBytes {
			hit.Text = hit.Text[:validUTF8Cut(hit.Text, maxSearchTextBytes)]
			hit.Truncated = true
		}
		cost := len(hit.SourceID) + len(hit.AppID) + len(hit.Domain) + len(hit.Text)
		if cost > maxSearchOutputBytes-used {
			output.Truncated = true
			break
		}
		used += cost
		output.Truncated = output.Truncated || hit.Truncated
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
