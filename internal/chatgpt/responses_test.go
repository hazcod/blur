package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

const completeFixture = "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_fixture\",\"output_index\":0,\"content_index\":0,\"delta\":\"A short answer.\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"A short answer.\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":4}}}\n\n"

func (f *authFixture) stream(body string, onRequest func(*http.Request)) {
	previous := f.manager.client.Transport
	f.manager.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != resource+"/responses" {
			return previous.RoundTrip(r)
		}
		if onRequest != nil {
			onRequest(r)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
}
func TestResponsesUsesSanctionedWireShapeAndRequiresCompletedText(t *testing.T) {
	f := newAuthFixture(t)
	a := f.login("")
	f.stream(completeFixture, func(r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer PRIVATE_ACCESS" {
			t.Error("request lacked selected account")
		}
		var wire map[string]any
		if json.NewDecoder(r.Body).Decode(&wire) != nil {
			t.Fatal("invalid request JSON")
		}
		for _, key := range []string{"max_output_tokens", "temperature", "top_p", "previous_response_id", "conversation", "background", "messages", "tools"} {
			if _, ok := wire[key]; ok {
				t.Errorf("unsupported SIWC field %s", key)
			}
		}
		if wire["store"] != false || wire["stream"] != true || wire["model"] != "fixture-model" || wire["instructions"] != "Use only admitted evidence." {
			t.Error("unsupported subscription request shape")
		}
		input, ok := wire["input"].([]any)
		if !ok || len(input) != 1 || input[0].(map[string]any)["role"] != "user" {
			t.Error("input was not explicit user context")
		}
	})
	out, err := f.manager.Generate(context.Background(), a.ID, CompletionRequest{Model: "fixture-model", Instructions: "Use only admitted evidence.", Input: "Synthetic private question", MaxOutputBytes: 1024})
	if err != nil || out.Text != "A short answer." || out.InputTokens != 10 || out.OutputTokens != 4 {
		t.Fatalf("completion=%+v err=%v", out, err)
	}
}
func TestResponsesNeverPublishesPartialFailedOrInconsistentStreams(t *testing.T) {
	delta := "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_fixture\",\"output_index\":0,\"content_index\":0,\"delta\":\"A short answer.\"}\n\n"
	for _, test := range []struct {
		name, stream string
		want         error
	}{
		{"interrupted", delta, ErrIncomplete},
		{"incomplete", delta + "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\"}}\n\n", ErrIncomplete},
		{"quota after delta", delta + "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"subscription_sharing_usage_limit_exceeded\",\"message\":\"PRIVATE_TEXT\"}}}\n\n", ErrUnavailable},
		{"different item", strings.Replace(completeFixture, "\"item_id\":\"msg_fixture\"", "\"item_id\":\"other\"", 1), ErrProtocol},
		{"different final text", strings.Replace(completeFixture, "\"text\":\"A short answer.\"", "\"text\":\"Changed text.\"", 1), ErrProtocol},
		{"tool output", strings.Replace(completeFixture, "\"type\":\"message\"", "\"type\":\"function_call\"", 1), ErrProtocol},
		{"missing successful status", strings.ReplaceAll(completeFixture, "\"status\":\"completed\",", ""), ErrProtocol},
		{"oversized record", "data: " + strings.Repeat("x", 65<<10) + "\n\n", ErrProtocol},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newAuthFixture(t)
			a := f.login("")
			f.stream(test.stream, nil)
			out, err := f.manager.Generate(context.Background(), a.ID, CompletionRequest{Model: "fixture-model", Instructions: "Instructions", Input: "Question", MaxOutputBytes: 1024})
			if out.Text != "" || !errors.Is(err, test.want) {
				t.Fatalf("partial stream escaped: output=%+v err=%v", out, err)
			}
			if err != nil && strings.Contains(err.Error(), "PRIVATE_TEXT") {
				t.Fatal("provider private text leaked in error")
			}
		})
	}
}
func TestResponsesLocalByteLimitClosesStream(t *testing.T) {
	f := newAuthFixture(t)
	a := f.login("")
	f.stream(completeFixture, nil)
	out, err := f.manager.Generate(context.Background(), a.ID, CompletionRequest{Model: "fixture-model", Instructions: "Instructions", Input: "Question", MaxOutputBytes: 4})
	if out.Text != "" || !errors.Is(err, ErrOutputLimit) {
		t.Fatal("output byte admission ignored", out, err)
	}
}
func TestResponsesRejectsUnentitledModelWithoutInference(t *testing.T) {
	f := newAuthFixture(t)
	a := f.login("")
	var calls atomic.Int32
	f.stream(completeFixture, func(*http.Request) { calls.Add(1) })
	if _, err := f.manager.Generate(context.Background(), a.ID, CompletionRequest{Model: "hidden", Instructions: "Instructions", Input: "Question", MaxOutputBytes: 1024}); !errors.Is(err, ErrConfig) || calls.Load() != 0 {
		t.Fatal("unlisted model caused inference", err)
	}
}

type contextBody struct {
	ctx    context.Context
	closed atomic.Bool
}

func (b *contextBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (b *contextBody) Close() error             { b.closed.Store(true); return nil }
func TestAccountRetirementCancelsInflightStreamsAndPreventsPublication(t *testing.T) {
	for _, action := range []string{"disconnect", "close"} {
		t.Run(action, func(t *testing.T) {
			f := newAuthFixture(t)
			a := f.login("")
			previous := f.manager.client.Transport
			entered := make(chan struct{})
			var body *contextBody
			f.manager.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != resource+"/responses" {
					return previous.RoundTrip(r)
				}
				body = &contextBody{ctx: r.Context()}
				close(entered)
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
			})
			done := make(chan error, 1)
			go func() {
				out, err := f.manager.Generate(context.Background(), a.ID, CompletionRequest{Model: "fixture-model", Instructions: "Instructions", Input: "Question", MaxOutputBytes: 1024})
				if out.Text != "" {
					done <- errors.New("retired account published output")
					return
				}
				done <- err
			}()
			<-entered
			if action == "disconnect" {
				confirmed, err := f.manager.Disconnect(context.Background(), a.ID)
				if err != nil || !confirmed {
					t.Fatal("synthetic revocation failed", err)
				}
			} else {
				f.manager.Close()
			}
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal("retirement did not cancel inference", err)
			}
			if !body.closed.Load() {
				t.Fatal("cancelled stream remained open")
			}
			if _, err := f.manager.ListModels(context.Background(), a.ID); err == nil {
				t.Fatal("retired account accepted a new request")
			}
		})
	}
}
