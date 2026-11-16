package model

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type localCountFallback struct {
	count        int
	system, user string
}

func (f *localCountFallback) Generate(context.Context, Request) (Response, error) {
	return Response{}, nil
}
func (f *localCountFallback) CountInputTokens(_ context.Context, system, user string) (int, error) {
	f.system, f.user = system, user
	return f.count, nil
}
func (f *localCountFallback) CloseIdleConnections()                    {}
func (f *localCountFallback) Identity(context.Context) (string, error) { return "synthetic", nil }

type localRequestCount struct {
	*localCountFallback
	request Request
}

func (f *localRequestCount) CountRequestTokens(_ context.Context, request Request) (int, error) {
	f.request = request
	return 37, nil
}

type localCapacity struct {
	limit int
	err   error
}

func (f localCapacity) ContextTokenLimit(context.Context) (int, error) { return f.limit, f.err }

func TestCountRequestInputTokensUsesFullRequestPortAndPreservesFallback(t *testing.T) {
	request := Request{System: "policy", User: "source", Format: "json", Schema: json.RawMessage(`{"type":"object"}`), MaxOutputTokens: 256}
	client := &localRequestCount{localCountFallback: &localCountFallback{count: 10}}
	count, err := CountRequestInputTokens(context.Background(), client, request)
	if err != nil || count != 37 || client.request.System != request.System || client.request.User != request.User || client.request.Format != "json" || string(client.request.Schema) != string(request.Schema) || client.request.MaxOutputTokens != 256 {
		t.Fatalf("request count = %d, forwarded = %#v, error = %v", count, client.request, err)
	}
	fallback := &localCountFallback{count: 10}
	count, err = CountRequestInputTokens(context.Background(), fallback, request)
	if err != nil || count != 10 || fallback.system != "policy" || fallback.user != "source" {
		t.Fatalf("fallback count = %d, error = %v", count, err)
	}
	if _, err := CountRequestInputTokens(nil, fallback, request); !errors.Is(err, ErrLocalCompletionConfig) { //nolint:staticcheck // Verify that the API rejects a nil context.
		t.Fatalf("nil context = %v", err)
	}
	if _, err := CountRequestInputTokens(context.Background(), nil, request); !errors.Is(err, ErrLocalCompletionConfig) {
		t.Fatalf("nil provider = %v", err)
	}
	fallback.count = -1
	if _, err := CountRequestInputTokens(context.Background(), fallback, request); !errors.Is(err, ErrLocalCompletionConfig) {
		t.Fatalf("negative count = %v", err)
	}
}

func TestRuntimeContextLimitSeparatesCapacityFromActualCounts(t *testing.T) {
	if limit, err := CompletionContextLimit(context.Background(), &localCountFallback{}); err != nil || limit != 8192 {
		t.Fatalf("legacy capacity = %d, %v", limit, err)
	}
	for _, test := range []struct {
		provider         any
		configured, want int
	}{
		{nil, 8192, 8192}, {struct{}{}, 4096, 4096},
		{localCapacity{limit: 4096}, 8192, 4096}, {localCapacity{limit: 8192}, 4096, 4096},
	} {
		limit, err := RuntimeContextLimit(context.Background(), test.provider, test.configured)
		if err != nil || limit != test.want {
			t.Fatalf("limit = %d, want %d, error %v", limit, test.want, err)
		}
	}
	for _, capacity := range []int{-1, 0, 512, 8193} {
		if _, err := RuntimeContextLimit(context.Background(), localCapacity{limit: capacity}, 8192); !errors.Is(err, ErrLocalCompletionConfig) {
			t.Fatalf("invalid capacity %d: %v", capacity, err)
		}
	}
	for _, fallback := range []int{0, 512, 8193} {
		if _, err := RuntimeContextLimit(context.Background(), nil, fallback); !errors.Is(err, ErrLocalCompletionConfig) {
			t.Fatalf("invalid fallback %d: %v", fallback, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RuntimeContextLimit(ctx, nil, 8192); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled = %v", err)
	}
	if _, err := RuntimeContextLimit(nil, nil, 8192); !errors.Is(err, ErrLocalCompletionConfig) { //nolint:staticcheck // Verify that the API rejects a nil context.
		t.Fatalf("nil context = %v", err)
	}
	if _, err := RuntimeContextLimit(context.Background(), localCapacity{err: ErrAppleUnavailable}, 8192); !errors.Is(err, ErrAppleUnavailable) {
		t.Fatalf("provider error = %v", err)
	}
}
