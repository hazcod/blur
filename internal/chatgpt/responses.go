package chatgpt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"
)

const (
	MaxRequestBytes   = 32 << 10
	MaxOutputBytes    = 12 << 10
	maxSSERecordBytes = 64 << 10
	maxStreamBytes    = 2 << 20
)

// MaxOutputBytes limits accepted local text. SIWC does not expose a supported
// per-request hard output-token cap; closing a stream is not a billing cap.
type CompletionRequest struct {
	Model, Instructions, Input string
	MaxOutputBytes             int
}

func (CompletionRequest) String() string   { return "[admitted ChatGPT request]" }
func (CompletionRequest) GoString() string { return "[admitted ChatGPT request]" }

type Completion struct {
	Text                      string
	InputTokens, OutputTokens int
}

func (Completion) String() string   { return "[completed ChatGPT response]" }
func (Completion) GoString() string { return "[completed ChatGPT response]" }
func (m *Manager) Generate(parent context.Context, id string, input CompletionRequest) (Completion, error) {
	if !validModelSlug(input.Model) || input.Instructions == "" || input.Input == "" || !utf8.ValidString(input.Instructions) || !utf8.ValidString(input.Input) || strings.ContainsRune(input.Instructions, 0) || strings.ContainsRune(input.Input, 0) || len(input.Instructions)+len(input.Input) > MaxRequestBytes || input.MaxOutputBytes < 1 || input.MaxOutputBytes > MaxOutputBytes {
		return Completion{}, ErrConfig
	}
	wire := struct {
		Model        string `json:"model"`
		Instructions string `json:"instructions"`
		Input        []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"input"`
		Store  bool `json:"store"`
		Stream bool `json:"stream"`
	}{Model: input.Model, Instructions: input.Instructions, Stream: true}
	wire.Input = append(wire.Input, struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{Role: "user", Content: input.Input})
	encoded, err := json.Marshal(wire)
	if err != nil || len(encoded) > MaxRequestBytes {
		clear(encoded)
		return Completion{}, ErrConfig
	}
	defer clear(encoded)
	ctx, done, state, err := m.requestContext(parent, id)
	if err != nil {
		return Completion{}, err
	}
	defer done()
	access, err := m.accessToken(ctx, id, state)
	if err != nil {
		return Completion{}, err
	}
	models, err := m.listModels(ctx, access)
	if err != nil {
		return Completion{}, err
	}
	listed := false
	for _, model := range models {
		if model.Slug == input.Model {
			listed = true
			break
		}
	}
	if !listed {
		return Completion{}, ErrConfig
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, resource+"/responses", bytes.NewReader(encoded))
	if err != nil {
		return Completion{}, ErrConfig
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+access)
	response, err := m.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Completion{}, ctx.Err()
		}
		return Completion{}, ErrUnavailable
	}
	defer func(close func() error) { _ = close() }(response.Body.Close)
	if response.StatusCode != http.StatusOK {
		data, readErr := io.ReadAll(io.LimitReader(response.Body, maxSSERecordBytes+1))
		defer clear(data)
		if readErr != nil || len(data) > maxSSERecordBytes {
			return Completion{}, ErrProtocol
		}
		return Completion{}, decodeAPIError(response, data)
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "text/event-stream" {
		return Completion{}, ErrProtocol
	}
	out, err := readCompletion(ctx, response.Body, input.MaxOutputBytes, response.Header.Get("x-request-id"))
	if state.ctx.Err() != nil {
		return Completion{}, context.Canceled
	}
	if ctx.Err() != nil {
		return Completion{}, ctx.Err()
	}
	return out, err
}

type streamEvent struct {
	Type         string          `json:"type"`
	ItemID       string          `json:"item_id"`
	OutputIndex  *int            `json:"output_index"`
	ContentIndex *int            `json:"content_index"`
	Delta        *string         `json:"delta"`
	Response     json.RawMessage `json:"response"`
	Error        json.RawMessage `json:"error"`
	Code         string          `json:"code"`
}
type completedResponse struct {
	Status string          `json:"status"`
	Error  json.RawMessage `json:"error"`
	Output []struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Status  string `json:"status"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		Input  int `json:"input_tokens"`
		Output int `json:"output_tokens"`
	} `json:"usage"`
}

func readCompletion(ctx context.Context, body io.Reader, limit int, requestID string) (Completion, error) {
	scanner := bufio.NewScanner(io.LimitReader(body, maxStreamBytes+1))
	scanner.Buffer(make([]byte, 4096), maxSSERecordBytes+1)
	data := make([]byte, 0, 4096)
	defer func() { clear(data) }()
	var text bytes.Buffer
	defer func() { clear(text.Bytes()) }()
	itemID := ""
	outputIndex := -1
	contentIndex := -1
	seenDelta := false
	total := 0
	dispatch := func() (Completion, bool, error) {
		if len(data) == 0 {
			return Completion{}, false, nil
		}
		if string(data) == "[DONE]" {
			return Completion{}, false, ErrIncomplete
		}
		var event streamEvent
		if !utf8.Valid(data) || json.Unmarshal(data, &event) != nil {
			return Completion{}, false, ErrProtocol
		}
		switch event.Type {
		case "response.output_text.delta":
			if event.Delta == nil || event.ItemID == "" || !safeText(event.ItemID, 256) || event.OutputIndex == nil || event.ContentIndex == nil || *event.OutputIndex < 0 || *event.OutputIndex > 32 || *event.ContentIndex != 0 || !utf8.ValidString(*event.Delta) || strings.ContainsRune(*event.Delta, 0) {
				return Completion{}, false, ErrProtocol
			}
			if !seenDelta {
				itemID, outputIndex, contentIndex = event.ItemID, *event.OutputIndex, *event.ContentIndex
				seenDelta = true
			} else if itemID != event.ItemID || outputIndex != *event.OutputIndex || contentIndex != *event.ContentIndex {
				return Completion{}, false, ErrProtocol
			}
			if len(*event.Delta) > limit-text.Len() {
				return Completion{}, false, ErrOutputLimit
			}
			_, _ = text.WriteString(*event.Delta)
		case "response.completed":
			var final completedResponse
			if json.Unmarshal(event.Response, &final) != nil || final.Status != "completed" || len(final.Error) > 0 && string(final.Error) != "null" || len(final.Output) < 1 || len(final.Output) > 32 || final.Usage.Input < 0 || final.Usage.Output < 0 || final.Usage.Input > 10_000_000 || final.Usage.Output > 10_000_000 {
				return Completion{}, false, ErrProtocol
			}
			messageCount := 0
			answer := ""
			for i, message := range final.Output {
				if message.Type == "reasoning" {
					continue
				}
				if message.Type != "message" || message.Role != "assistant" || message.Status != "completed" || len(message.Content) != 1 || message.Content[0].Type != "output_text" {
					return Completion{}, false, ErrProtocol
				}
				messageCount++
				answer = message.Content[0].Text
				if seenDelta && (message.ID != itemID || i != outputIndex || contentIndex != 0) {
					return Completion{}, false, ErrProtocol
				}
			}
			if messageCount != 1 || answer == "" || !utf8.ValidString(answer) || strings.ContainsRune(answer, 0) || seenDelta && answer != text.String() {
				return Completion{}, false, ErrProtocol
			}
			if len(answer) > limit {
				return Completion{}, false, ErrOutputLimit
			}
			return Completion{Text: answer, InputTokens: final.Usage.Input, OutputTokens: final.Usage.Output}, true, nil
		case "response.failed", "error":
			payload := data
			if event.Type == "response.failed" {
				payload = event.Response
			}
			header := http.Header{}
			if safeText(requestID, 128) {
				header.Set("x-request-id", requestID)
			}
			return Completion{}, false, decodeAPIError(&http.Response{StatusCode: 200, Header: header}, payload)
		case "response.incomplete":
			return Completion{}, false, ErrIncomplete
		case "response.refusal.delta", "response.refusal.done":
			return Completion{}, false, ErrProtocol
		default:
			// Other documented Responses lifecycle/reasoning events do not carry
			// accepted text. Unknown event names cannot publish a result by themselves.
			if !strings.HasPrefix(event.Type, "response.") {
				return Completion{}, false, ErrProtocol
			}
		}
		return Completion{}, false, nil
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return Completion{}, err
		}
		line := scanner.Bytes()
		total += len(line) + 1
		if total > maxStreamBytes {
			return Completion{}, ErrProtocol
		}
		if len(line) == 0 {
			out, done, err := dispatch()
			clear(data)
			data = data[:0]
			if err != nil {
				return Completion{}, err
			}
			if done {
				return out, nil
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			part := line[5:]
			if len(part) > 0 && part[0] == ' ' {
				part = part[1:]
			}
			if len(data)+len(part)+1 > maxSSERecordBytes {
				return Completion{}, ErrProtocol
			}
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, part...)
		}
	}
	if ctx.Err() != nil {
		return Completion{}, ctx.Err()
	}
	if scanner.Err() != nil {
		return Completion{}, ErrProtocol
	}
	return Completion{}, ErrIncomplete
}
