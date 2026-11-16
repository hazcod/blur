package assistant

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/model"
)

func TestGroundedAnswerDiagnosticsExposeOnlyClosedCodesAndCounts(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{{ErrGeneratorResponse, "generated_structure"}, {ErrAnswer, "answer_validation"}, {model.ErrAppleResponse, "apple_response"}, {errors.New("PRIVATE QUESTION RESPONSE SOURCE HANDLE"), "unknown"}} {
		var output bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&output, nil))
		logGroundedAnswerFailure(logger, tc.err, 3)
		text := output.String()
		if !strings.Contains(text, `"reason":"`+tc.code+`"`) || !strings.Contains(text, `"source_count":3`) || strings.Contains(text, "PRIVATE") || strings.Contains(text, tc.err.Error()) {
			t.Fatal("grounded answer diagnostics leaked a payload or lost the closed failure code")
		}
	}
}
