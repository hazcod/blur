package client

import (
	"strings"
	"testing"
)

func TestBackgroundWorkRecapsRowShowsRollupCounters(t *testing.T) {
	row := formatBackgroundWorkGroup(BackgroundWorkGroup{ID: "recaps", Enabled: true, State: "idle", RollupsPending: 2, RollupsDirty: 3})
	if !strings.Contains(row.Detail, "2 day or week recaps pending.") {
		t.Fatalf("pending rollups not shown: %q", row.Detail)
	}
	row = formatBackgroundWorkGroup(BackgroundWorkGroup{ID: "recaps", Enabled: true, State: "idle", RollupsDirty: 1})
	if !strings.Contains(row.Detail, "1 day or week recap waiting for its period to end.") {
		t.Fatalf("dirty rollups not shown: %q", row.Detail)
	}
	row = formatBackgroundWorkGroup(BackgroundWorkGroup{ID: "library", Enabled: true, State: "idle", RollupsPending: 2})
	if strings.Contains(row.Detail, "recap") {
		t.Fatalf("rollup counters leaked into another group: %q", row.Detail)
	}
}
