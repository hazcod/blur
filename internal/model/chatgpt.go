package model

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/chatgpt"
)

var (
	ErrChatGPTConfig     = errors.New("model: invalid ChatGPT account configuration")
	ErrChatGPTCapability = errors.New("model: ChatGPT plan requires explicit client output-limit admission")
)

// ChatGPTOptions acknowledges that SIWC supports a local accepted-output byte
// limit and cancellation, without a provider-enforced output-token/spend cap.
type ChatGPTOptions struct{ AllowClientOutputLimit bool }
type ChatGPTCapabilities struct {
	HardOutputTokenLimit  bool
	ClientOutputByteLimit int
}
type chatGPTAccounts interface {
	Accounts(context.Context) ([]chatgpt.Account, error)
	Generate(context.Context, string, chatgpt.CompletionRequest) (chatgpt.Completion, error)
	CloseIdleConnections()
}

// ChatGPT satisfies the completion port but is a remote account provider. Its
// identity binds the registration and chosen model. Construction does no I/O.
type ChatGPT struct {
	accounts         chatGPTAccounts
	accountID, model string
}

func (*ChatGPT) String() string   { return "[ChatGPT completion provider]" }
func (*ChatGPT) GoString() string { return "[ChatGPT completion provider]" }
func NewChatGPT(accounts chatGPTAccounts, accountID, modelName string, options ChatGPTOptions) (*ChatGPT, error) {
	decoded, err := hex.DecodeString(accountID)
	if accounts == nil || err != nil || len(decoded) != 32 || strings.ToLower(accountID) != accountID || modelName == "" || len(modelName) > 128 || !utf8.ValidString(modelName) || strings.ContainsAny(modelName, "\x00\r\n\t ") {
		return nil, ErrChatGPTConfig
	}
	if !options.AllowClientOutputLimit {
		return nil, ErrChatGPTCapability
	}
	return &ChatGPT{accounts: accounts, accountID: accountID, model: modelName}, nil
}
func (m *ChatGPT) Capabilities() ChatGPTCapabilities {
	return ChatGPTCapabilities{HardOutputTokenLimit: false, ClientOutputByteLimit: MaxGeneratedContentBytes}
}
func (m *ChatGPT) CloseIdleConnections() {
	if m != nil && m.accounts != nil {
		m.accounts.CloseIdleConnections()
	}
}
func (m *ChatGPT) Identity(ctx context.Context) (string, error) {
	if m == nil || m.accounts == nil || ctx == nil {
		return "", ErrChatGPTConfig
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	accounts, err := m.accounts.Accounts(ctx)
	if err != nil {
		return "", err
	}
	for _, a := range accounts {
		if a.ID == m.accountID {
			if !a.Connected {
				return "", chatgpt.ErrSignedOut
			}
			if !a.InferenceEnabled {
				return "", chatgpt.ErrPermission
			}
			return "chatgpt:" + a.ID + ":" + m.model, nil
		}
	}
	return "", chatgpt.ErrSignedOut
}
func (m *ChatGPT) CountInputTokens(ctx context.Context, system, user string) (int, error) {
	if m == nil || ctx == nil || len(system)+len(user) > MaxRequestBytes || !utf8.ValidString(system) || !utf8.ValidString(user) || strings.ContainsRune(system, 0) || strings.ContainsRune(user, 0) {
		return 0, ErrChatGPTConfig
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return len(system) + len(user), nil
}
func (m *ChatGPT) CountRequestTokens(ctx context.Context, input Request) (int, error) {
	instructions, err := chatGPTInstructions(input)
	if err != nil {
		return 0, err
	}
	return m.CountInputTokens(ctx, instructions, input.User)
}
func (m *ChatGPT) Generate(ctx context.Context, input Request) (Response, error) {
	if input.MaxOutputTokens < 1 || input.MaxOutputTokens > MaxCompletionTokens {
		return Response{}, ErrChatGPTConfig
	}
	instructions, err := chatGPTInstructions(input)
	if err != nil {
		return Response{}, err
	}
	if _, err := m.CountInputTokens(ctx, instructions, input.User); err != nil {
		return Response{}, err
	}
	if instructions == "" || input.User == "" {
		return Response{}, ErrChatGPTConfig
	}
	if _, err := m.Identity(ctx); err != nil {
		return Response{}, err
	}
	completed, err := m.accounts.Generate(ctx, m.accountID, chatgpt.CompletionRequest{Model: m.model, Instructions: instructions, Input: input.User, MaxOutputBytes: min(MaxGeneratedContentBytes, input.MaxOutputTokens*4)})
	if err != nil {
		return Response{}, err
	}
	if completed.Text == "" || len(completed.Text) > min(MaxGeneratedContentBytes, input.MaxOutputTokens*4) || !utf8.ValidString(completed.Text) || strings.ContainsRune(completed.Text, 0) {
		return Response{}, chatgpt.ErrProtocol
	}
	return Response{Text: completed.Text, InputTokens: completed.InputTokens, OutputTokens: completed.OutputTokens}, nil
}
func chatGPTInstructions(input Request) (string, error) {
	if input.Format != "" && input.Format != "json" || input.KeepAlive < 0 || input.KeepAlive > 10*time.Minute || len(input.Schema) > 16<<10 {
		return "", ErrChatGPTConfig
	}
	instructions := input.System
	if len(input.Schema) > 0 {
		var schema map[string]any
		if !utf8.Valid(input.Schema) || strings.IndexByte(string(input.Schema), 0) >= 0 || json.Unmarshal(input.Schema, &schema) != nil || schema == nil {
			return "", ErrChatGPTConfig
		}
		instructions += "\nReturn one JSON value conforming to this schema, with no surrounding text:\n" + string(input.Schema)
	} else if input.Format == "json" {
		instructions += "\nReturn one JSON object with no surrounding text."
	}
	return instructions, nil
}

var _ LocalCompletion = (*ChatGPT)(nil)
var _ RequestTokenCounter = (*ChatGPT)(nil)
