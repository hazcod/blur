package client

import (
	"testing"
	"time"
)

func TestAskOperationTimeoutAllowsWholeDocumentReductionToFinish(t *testing.T) {
	if got := askOperationTimeout("Summarize the whole page about architecture"); got != wholeDocumentTimeout {
		t.Fatalf("whole-document analysis timeout = %s, want %s", got, wholeDocumentTimeout)
	}
	if got := askOperationTimeout("What was I doing today?"); got != noteOperationTimeout {
		t.Fatalf("ordinary Ask timeout = %s, want %s", got, noteOperationTimeout)
	}
	if wholeDocumentTimeout <= time.Minute {
		t.Fatalf("whole-document timeout is not materially longer than ordinary Ask: %s", wholeDocumentTimeout)
	}
}
