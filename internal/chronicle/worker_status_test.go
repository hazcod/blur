package chronicle

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/hazcod/blur/internal/model"
)

func TestWorkerStatusClassifiesAppleAndSummaryValidationFailures(t *testing.T) {
	for _, test := range []struct {
		err  error
		code string
	}{
		{ErrInvalid, "invalid_output"},
		{model.ErrAppleResponse, "invalid_output"},
		{model.ErrAppleRefused, "model_refused"},
		{model.ErrAppleConfig, "model_configuration"},
		{errors.New("private prompt or backend detail"), "processing_failed"},
	} {
		worker := &Worker{ctx: context.Background()}
		worker.recordAttempt(test.err)
		status := worker.Status()
		if status.LastErrorCode != test.code || status.LastErrorAt.IsZero() {
			t.Fatalf("closed classification=%s, want %s", status.LastErrorCode, test.code)
		}
	}
}

func TestWorkerFailureLogContainsOnlyClosedDiagnosticCode(t *testing.T) {
	handler := &captureHandler{}
	worker := &Worker{logger: slog.New(handler)}
	worker.logJobError(errors.New("private source or backend detail"))
	if len(handler.records) != 1 || handler.records[0].Message != "chronicle job failed" {
		t.Fatal("failure diagnostic missing")
	}
	code := ""
	handler.records[0].Attrs(func(attribute slog.Attr) bool {
		if attribute.Key == "code" {
			code = attribute.Value.String()
		}
		if attribute.Key != "code" && (attribute.Key != "result" || attribute.Value.String() != "failed") {
			t.Fatal("raw backend detail escaped the closed diagnostic")
		}
		return true
	})
	if code != "processing_failed" {
		t.Fatal("unknown failure was not mapped to a closed code")
	}
}
