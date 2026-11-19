package chronicle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/hazcod/blur/internal/model"
)

const appleRollupSchema = `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`

type rollupResponse struct {
	Text string `json:"text"`
}

func (g *OllamaGenerator) rollupRequest(prompt RollupPrompt, maxOutputTokens int) (model.Request, error) {
	if g == nil || g.client == nil || prompt.System == "" || prompt.User == "" {
		return model.Request{}, ErrInvalid
	}
	request := model.Request{System: prompt.System, User: prompt.User, Format: "json", MaxOutputTokens: maxOutputTokens}
	if len(g.schema) > 0 {
		request.Format, request.Schema = "", json.RawMessage(appleRollupSchema)
	}
	return request, nil
}

// CountRollupTokens reports the input cost of a rollup request.
func (g *OllamaGenerator) CountRollupTokens(ctx context.Context, prompt RollupPrompt) (int, error) {
	request, err := g.rollupRequest(prompt, 0)
	if err != nil {
		return 0, err
	}
	return model.CountRequestInputTokens(ctx, g.client, request)
}

// GenerateRollup returns the untrusted recap text of a rollup request.
func (g *OllamaGenerator) GenerateRollup(ctx context.Context, prompt RollupPrompt, maxOutputTokens int) (string, error) {
	request, err := g.rollupRequest(prompt, maxOutputTokens)
	if err != nil {
		return "", err
	}
	response, err := g.client.Generate(ctx, request)
	if err != nil {
		return "", err
	}
	var decoded rollupResponse
	decoder := json.NewDecoder(bytes.NewBufferString(response.Text))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&decoded) != nil {
		return "", model.ErrOllamaResponse
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", model.ErrOllamaResponse
	}
	return decoded.Text, nil
}

// RollupModel names the local model that wrote a rollup.
func (g *OllamaGenerator) RollupModel(ctx context.Context) (string, error) {
	if g == nil || g.client == nil {
		return "", ErrInvalid
	}
	return g.client.Identity(ctx)
}
