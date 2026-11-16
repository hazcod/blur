package chronicle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/power"
)

func TestRecapPromptSeparatesVisibleContentFromUserActivity(t *testing.T) {
	g := &OllamaGenerator{client: &appleCompletionStub{}}
	p, err := PackHour(workerHour(t))
	if err != nil {
		t.Fatal(err)
	}
	system, _, err := g.request(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"visible content", "not proof", "watched", "used a tool", "describes"} {
		if !strings.Contains(system, required) {
			t.Fatalf("recap attribution rule missing: %s", required)
		}
	}
}

func legacyRecapReceipt(p Packed) string {
	type item struct {
		ID, App, Domain, Text string
		At                    time.Time
		Salience              uint8
		OriginalBytes         int
		Partial               bool
	}
	receipt := struct {
		Version int
		Hour    string
		Sources []item
		Omitted int
		Limited bool
	}{Version: 1, Hour: p.HourID, Omitted: p.Omitted, Limited: p.SourceLimitReached}
	for _, e := range p.Evidence {
		receipt.Sources = append(receipt.Sources, item{e.SourceID(), e.AppID(), e.Domain(), e.Text(), e.At.UTC(), e.Salience, e.OriginalBytes(), e.CapturedPartial()})
	}
	raw, _ := json.Marshal(receipt)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func TestRecapAttributionPromptRetiresOldInputReceipts(t *testing.T) {
	hour := workerHour(t)
	packed, err := PackHour(hour)
	if err != nil {
		t.Fatal(err)
	}
	old := legacyRecapReceipt(packed)
	if old == inputReceipt(packed) {
		t.Fatal("old attribution prompt receipt is still current")
	}
	repo := &workerRepo{rows: map[string]Summary{packed.HourID: {HourID: packed.HourID, Start: packed.Start, End: packed.End, Text: "An old generated recap.", SourceIDs: packed.SourceIDs, InputReceipt: old}}}
	model := &workerModel{count: 10}
	worker := NewWorker(repo, &workerPolicy{value: performance()}, model, WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	defer worker.Close()
	if err := worker.Schedule(context.Background(), hour); err != nil {
		t.Fatal(err)
	}
	waitReceiptWorker(t, worker, repo, hour)
	if model.callCount() != 1 {
		t.Fatal("old attribution recap was not regenerated")
	}
}
