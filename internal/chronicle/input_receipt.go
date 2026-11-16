package chronicle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// inputReceipt binds the exact bounded source selection and its coverage.
// A generated recap may cite a subset, so citation equality cannot establish
// that newly indexed parts have already been processed.
func inputReceipt(p Packed) string {
	type item struct {
		ID, App, Domain, Text string
		At                    time.Time
		Salience              uint8
		OriginalBytes         int
		Partial               bool
	}
	receipt := struct {
		Version int
		Prompt  string
		Hour    string
		Sources []item
		Omitted int
		Limited bool
	}{Version: 2, Prompt: summarySystemPrompt, Hour: p.HourID, Omitted: p.Omitted, Limited: p.SourceLimitReached}
	for _, e := range p.Evidence {
		receipt.Sources = append(receipt.Sources, item{e.SourceID(), e.AppID(), e.Domain(), e.Text(), e.At.UTC(), e.Salience, e.OriginalBytes(), e.CapturedPartial()})
	}
	raw, _ := json.Marshal(receipt)
	defer clear(raw)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}
