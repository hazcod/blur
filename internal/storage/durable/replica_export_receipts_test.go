package durable

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/securestore"
)

const receiptTestGeneration = "00000000-0000-4000-8000-000000000001"

func TestReplicaReceiptDeletionAllowsAbsentAndPrunesPresentReceipt(t *testing.T) {
	for _, ack := range []bool{false, true} {
		t.Run(fmt.Sprint(ack), func(t *testing.T) {
			ctx := context.Background()
			s := openReplicaTestStore(t)
			o, err := capture.SanitizeObservation("editor", "", "source erased with optional receipt", 1024)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.PutObservationAt(ctx, o, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if ack {
				page, err := s.ExportReplicaObservations(ctx, receiptTestGeneration, nil, 10, 1<<20)
				if err != nil || len(page.Items) != 1 {
					t.Fatal(page, err)
				}
				if err := s.AcknowledgeReplicaExport(ctx, receiptTestGeneration, page.Items, false); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.DeleteObservationRecord(ctx, o.ContentID()); err != nil {
				t.Fatal("ordinary erasure rejected absent or exported receipt", err)
			}
			key := s.logicalKey(groupRecord, kindObservation, "", o.ContentID())
			defer clear(key)
			b, found, err := s.readIndexValue(ctx, replicaExportNS, replicaReceiptID(key), 48)
			clear(b)
			if err != nil || found {
				t.Fatal("source erasure retained receipt metadata", found, err)
			}
		})
	}
}

func TestReplicaReceiptCASPreservesSameIDSameTimeRewriteAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.db")
	s, err := OpenDurable(path, securestore.OfflineFixtureKey(), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	o, _ := capture.SanitizeObservation("editor", "", "same source captured again at identical wall time", 1024)
	at := time.Now().UTC()
	if err := s.PutObservationAt(ctx, o, at); err != nil {
		t.Fatal(err)
	}
	old, err := s.ExportReplicaObservations(ctx, receiptTestGeneration, nil, 10, 1<<20)
	if err != nil || len(old.Items) != 1 {
		t.Fatal(err)
	}
	// Rewrite authenticated provenance without changing source identity or wall
	// time. This exercises the receipt CAS independently of source admission,
	// which correctly rejects duplicate public PutObservationAt calls.
	if err := s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		orderKey := s.logicalKey(groupOrder, kindObservation, "", o.ContentID())
		recordKey := s.logicalKey(groupRecord, kindObservation, "", o.ContentID())
		defer clear(orderKey)
		defer clear(recordKey)
		value, err := s.encodeSizeEntry(orderKey, recordKey, len(o.Text())+len(o.AppID())+len(o.Domain()), at)
		defer clear(value)
		if err != nil {
			return err
		}
		return tx.writeEntryLocked(orderKey, value, true)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeReplicaExport(ctx, receiptTestGeneration, old.Items, false); err != nil {
		t.Fatal(err)
	}
	current, err := s.ExportReplicaObservations(ctx, receiptTestGeneration, nil, 10, 1<<20)
	if err != nil || len(current.Items) != 1 || current.Items[0].Revision == old.Items[0].Revision {
		t.Fatal("old receipt poisoned a rewritten source", err)
	}
	if err := s.AcknowledgeReplicaExport(ctx, receiptTestGeneration, current.Items, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, securestore.OfflineFixtureKey(), OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ExportReplicaObservations(ctx, receiptTestGeneration, nil, 10, 1<<20)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("acknowledged exact revision did not survive encrypted reopen", err)
	}
	page, err = s.ExportReplicaObservations(ctx, "00000000-0000-4000-8000-000000000002", nil, 10, 1<<20)
	if err != nil || len(page.Items) != 1 {
		t.Fatal("new folder generation inherited old export receipts", err)
	}
}

func TestReplicaReceiptScanCheckpointsLargeAcknowledgedPrefix(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	at := time.Now().UTC()
	// Actual encrypted rows in bounded write transactions, all sharing one time.
	for start := 0; start < exportScanRows+1; start += 8 {
		end := min(start+8, exportScanRows+1)
		if err := s.Update(ctx, func(tx *Tx) error {
			for i := start; i < end; i++ {
				o, _ := capture.SanitizeObservation("editor", "", fmt.Sprintf("independently retained row %04d", i), 1024)
				if err := tx.PutObservationAt(o, at); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	var cursor []byte
	count := 0
	for pass := 0; pass < 3; pass++ {
		page, err := s.ExportReplicaObservations(ctx, receiptTestGeneration, cursor, 4096, 12<<20)
		if err != nil {
			t.Fatal(err)
		}
		if page.Rows > exportScanRows {
			t.Fatal("one flush scanned an unbounded acknowledged prefix", page.Rows)
		}
		count += len(page.Items)
		if err := s.AcknowledgeReplicaExport(ctx, receiptTestGeneration, page.Items, false); err != nil {
			t.Fatal(err)
		}
		cursor = page.Next
		if !page.HasMore {
			break
		}
	}
	if count != exportScanRows+1 {
		t.Fatal("equal-time boundary lost encrypted sources", count)
	}
	first, err := s.ExportReplicaObservations(ctx, receiptTestGeneration, nil, 4096, 12<<20)
	if err != nil || len(first.Items) != 0 || first.Rows != exportScanRows || !first.HasMore || len(first.Next) == 0 {
		t.Fatalf("acknowledged prefix did not yield bounded continuation: %+v %v", first, err)
	}
	second, err := s.ExportReplicaObservations(ctx, receiptTestGeneration, first.Next, 4096, 12<<20)
	if err != nil || len(second.Items) != 0 || second.Rows != 1 || second.HasMore || len(second.Next) != 0 {
		t.Fatalf("continuation failed to finish bounded sweep: %+v %v", second, err)
	}
}
