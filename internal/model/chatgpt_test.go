package model

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/chatgpt"
)

type chatGPTAccountFixture struct {
	accounts []chatgpt.Account
	request  chatgpt.CompletionRequest
	calls    int
	closes   int
}

func (f *chatGPTAccountFixture) Accounts(context.Context) ([]chatgpt.Account, error) {
	return f.accounts, nil
}
func (f *chatGPTAccountFixture) Generate(_ context.Context, _ string, r chatgpt.CompletionRequest) (chatgpt.Completion, error) {
	f.calls++
	f.request = r
	return chatgpt.Completion{Text: `{"answer":"Mira"}`, InputTokens: 20, OutputTokens: 4}, nil
}
func (f *chatGPTAccountFixture) CloseIdleConnections() { f.closes++ }
func TestChatGPTRequiresExplicitClientOutputLimitAdmission(t *testing.T) {
	f := &chatGPTAccountFixture{}
	if m, err := NewChatGPT(f, strings.Repeat("a", 64), "selected-model", ChatGPTOptions{}); !errors.Is(err, ErrChatGPTCapability) || m != nil {
		t.Fatal("unsupported hard token limit silently admitted", err)
	}
	if f.calls != 0 {
		t.Fatal("capability admission inferred")
	}
}
func TestChatGPTBindsSelectedRegistrationAndModelAndPreservesValidators(t *testing.T) {
	id := strings.Repeat("a", 64)
	f := &chatGPTAccountFixture{accounts: []chatgpt.Account{{ID: id, Connected: true, InferenceEnabled: true, ClientID: "issued-client"}}}
	m, err := NewChatGPT(f, id, "selected-model", ChatGPTOptions{AllowClientOutputLimit: true})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := m.Identity(context.Background())
	if err != nil || !strings.Contains(identity, id) || !strings.Contains(identity, "selected-model") {
		t.Fatal("identity omitted selected account/model", identity, err)
	}
	if m.Capabilities().HardOutputTokenLimit {
		t.Fatal("SIWC advertised a server token cap")
	}
	request := Request{System: "Use admitted evidence.", User: "Who owns rollback?", Format: "json", Schema: []byte(`{"type":"object","properties":{"answer":{"type":"string"}}}`), MaxOutputTokens: 64}
	out, err := m.Generate(context.Background(), request)
	if err != nil || out.Text != `{"answer":"Mira"}` || f.request.Model != "selected-model" || f.request.Input != request.User || f.request.MaxOutputBytes != 256 || !strings.Contains(f.request.Instructions, "Use admitted evidence.") || !strings.Contains(f.request.Instructions, string(request.Schema)) {
		t.Fatal("admitted request lost its constraints", out, err)
	}
	count, err := m.CountRequestTokens(context.Background(), request)
	if err != nil || count < len(request.System)+len(request.User)+len(request.Schema) {
		t.Fatal("schema admission was not counted", count, err)
	}
	m.CloseIdleConnections()
	if f.closes != 1 {
		t.Fatal("idle connections not retired")
	}
	f.accounts = nil
	if _, err := m.Generate(context.Background(), request); !errors.Is(err, chatgpt.ErrSignedOut) || f.calls != 1 {
		t.Fatal("signed-out account silently fell back", err)
	}
}
