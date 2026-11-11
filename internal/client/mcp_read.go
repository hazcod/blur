package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/localapi"
)

const (
	maxMCPReadResults     = 8
	maxMCPReadSourceBytes = 64 << 10
	maxMCPReadTextBytes   = 2048
	maxMCPReadOutputBytes = 16 << 10
)

type mcpSearchParams struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type mcpSearchResponse struct {
	Results   []SearchHit `json:"results"`
	Truncated bool        `json:"truncated"`
}

// HandleMCPRead adapts the single supported MCP query to the already-open
// encrypted store. It intentionally supports no writes and never logs memory.
func (c *Client) HandleMCPRead(ctx context.Context, method string, raw json.RawMessage) (json.RawMessage, error) {
	if c == nil || c.data == nil || ctx == nil {
		return nil, ErrNotConfigured
	}
	if method != localapi.RPCSearchMemory {
		return nil, ErrInvalidRequest
	}
	params, err := decodeMCPReadParams(raw)
	if err != nil || params.Limit < 1 || params.Limit > maxMCPReadResults || len(params.Query) > MaxQueryBytes || !utf8.ValidString(params.Query) || strings.IndexByte(params.Query, 0) >= 0 || strings.TrimSpace(params.Query) == "" {
		return nil, ErrInvalidRequest
	}
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return nil, ErrMemoryChanging
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	page, err := c.searchContextLimit(ctx, params.Query, params.Limit+1)
	if err != nil {
		return nil, err
	}
	response := mcpSearchResponse{Results: make([]SearchHit, 0, min(len(page.Hits), params.Limit)), Truncated: page.HasMore || len(page.Hits) > params.Limit}
	used := 0
	for _, hit := range page.Hits {
		if len(response.Results) >= params.Limit {
			response.Truncated = true
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if hit.Bytes < 1 || hit.Bytes > maxMCPReadSourceBytes-used {
			response.Truncated = true
			break
		}
		used += hit.Bytes
		observation, found, err := c.data.GetObservation(ctx, hit.SourceID)
		if err != nil {
			return nil, err
		}
		if !found {
			response.Truncated = true
			continue
		}
		text := observation.Text()
		actualBytes := len(text) + len(observation.AppID()) + len(observation.Domain())
		if observation.ContentID() != hit.SourceID || actualBytes != hit.Bytes || observation.Validate() != nil || actualBytes > maxMCPReadSourceBytes {
			return nil, ErrInvalidRequest
		}
		text, clipped := truncateMCPReadText(text)
		item := SearchHit{SourceID: observation.ContentID(), AppID: observation.AppID(), Domain: observation.Domain(), Text: text, Truncated: clipped || observation.Truncated()}
		response.Results = append(response.Results, item)
		encoded, err := json.Marshal(response)
		if err != nil {
			return nil, ErrInvalidRequest
		}
		if len(encoded) > maxMCPReadOutputBytes {
			response.Results = response.Results[:len(response.Results)-1]
			response.Truncated = true
			break
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var encoded []byte
	if !c.PublishMemoryRead(epoch, func() {
		encoded, err = json.Marshal(response)
	}) {
		return nil, ErrMemoryChanging
	}
	if err != nil || len(encoded) > maxMCPReadOutputBytes {
		clear(encoded)
		return nil, ErrInvalidRequest
	}
	return encoded, nil
}

func decodeMCPReadParams(raw []byte) (mcpSearchParams, error) {
	if len(raw) == 0 || len(raw) > 2<<10 || !json.Valid(raw) {
		return mcpSearchParams{}, ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var params mcpSearchParams
	if decoder.Decode(&params) != nil {
		return mcpSearchParams{}, ErrInvalidRequest
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return mcpSearchParams{}, ErrInvalidRequest
	}
	return params, nil
}

func truncateMCPReadText(value string) (string, bool) {
	if len(value) <= maxMCPReadTextBytes {
		return value, false
	}
	cut := maxMCPReadTextBytes
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut], true
}
