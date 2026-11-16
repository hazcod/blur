package intent

import (
	"context"
	"errors"
	"testing"
)

func TestAppleAskClassificationPreservesSemanticModeWithoutDetailGeneration(t *testing.T) {
	for _, test := range []struct{ input, response, mode string }{
		{"Could you explain hash maps?", `{"kind":"ask_general"}`, "general"},
		{"Could you tell me who owns the Aurora rollback?", `{"kind":"ask_memory"}`, "memory"},
		{"What did I decide about Aurora?", `{"kind":"ask_memory"}`, "memory"},
	} {
		t.Run(test.mode+test.input, func(t *testing.T) {
			client := &appleCompletionStub{text: test.response}
			got, err := (&Ollama{client: client, apple: true}).Parse(context.Background(), Request{Input: test.input})
			if err != nil || got.Kind != Ask || got.Text != test.input || got.Mode != test.mode || client.calls != 1 {
				t.Fatalf("question routing repeated native inference or lost mode/input: %+v calls=%d err=%v", got, client.calls, err)
			}
		})
	}
}

func TestAppleKnownOperationSkipsRedundantKindGeneration(t *testing.T) {
	for _, test := range []struct {
		request Request
		reply   string
		kind    Kind
		text    string
	}{
		{Request{Input: "Move it to Friday at 9am", Targets: []Target{{Ref: "T1", Title: "Review Aurora"}}}, `{"kind":"task_edit","target":"T1","when":"Friday at 9am"}`, TaskEdit, ""},
		{Request{Input: "Move it to Friday at 9am", Targets: []Target{{Ref: "T1", Title: "Review Aurora"}, {Ref: "T2", Title: "Check backups"}}}, `{"kind":"clarify","text":"Which task should I move?"}`, Clarify, "Which task should I move?"},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			client := &appleCompletionStub{text: test.reply}
			got, err := (&Ollama{client: client, apple: true}).Parse(context.Background(), test.request)
			if err != nil || got.Kind != test.kind || got.Text != test.text || client.calls != 1 {
				t.Fatalf("known operation repeated kind inference: %+v calls=%d err=%v", got, client.calls, err)
			}
		})
	}
}

func TestAppleKnownPersonalTaskUsesItsProvenExactTitleWithoutInference(t *testing.T) {
	input := "I need to review the Aurora PR"
	client := &appleCompletionStub{err: errors.New("known task must not regenerate its proven exact title")}
	got, err := (&Ollama{client: client, apple: true}).Parse(context.Background(), Request{Input: input})
	if err != nil || got.Kind != Task || got.Text != "review the Aurora PR" || client.calls != 0 {
		t.Fatalf("known task waited or rewrote its action: %+v calls=%d err=%v", got, client.calls, err)
	}
}

func TestAppleExplicitNoteNeedsNoInferenceAndKeepsExactInput(t *testing.T) {
	input := "Can you save a note that Aurora uses port 4317?"
	client := &appleCompletionStub{err: errors.New("explicit note must not wait for the native model")}
	got, err := (&Ollama{client: client, apple: true}).Parse(context.Background(), Request{Input: input})
	if err != nil || got.Kind != Note || got.Text != input || client.calls != 0 {
		t.Fatalf("explicit note waited or rewrote original data: %+v calls=%d err=%v", got, client.calls, err)
	}
}

func TestAppleLocalNoteStillRejectsCanceledInterpretation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &appleCompletionStub{}
	_, err := (&Ollama{client: client, apple: true}).Parse(ctx, Request{Input: "Save a note that Aurora uses port 4317"})
	if !errors.Is(err, context.Canceled) || client.calls != 0 {
		t.Fatalf("canceled local note became actionable: calls=%d err=%v", client.calls, err)
	}
}

func TestAppleSemanticClassificationRejectsCrossKindFields(t *testing.T) {
	request := Request{Input: "Could you tell me who owns the Aurora rollback?"}
	for _, reply := range []string{
		`{"kind":"ask_memory","when":"tomorrow"}`,
		`{"kind":"ask_general","text":"rewritten request"}`,
		`{"kind":"ask_memory","mode":"general"}`,
		`{"kind":"ask_memory"} {}`,
	} {
		client := &appleCompletionStub{text: reply}
		if _, err := (&Ollama{client: client, apple: true}).Parse(context.Background(), request); !errors.Is(err, ErrInvalid) || client.calls != 1 {
			t.Fatalf("invalid classification escaped or retried: reply=%s calls=%d err=%v", reply, client.calls, err)
		}
	}
}

func TestAppleKnownOperationCannotChangeKindInItsDetailReply(t *testing.T) {
	client := &appleCompletionStub{text: `{"kind":"note"}`}
	_, err := (&Ollama{client: client, apple: true}).Parse(context.Background(), Request{Input: "Move it to Friday at 9am", Targets: []Target{{Ref: "T1", Title: "Review Aurora"}}})
	if !errors.Is(err, ErrInvalid) || client.calls != 1 {
		t.Fatalf("known task changed operation in details: calls=%d err=%v", client.calls, err)
	}
}
