package durable

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

type admissionWithoutBodies struct {
	bolt.Reader
	forbidden map[string]bool
}

func (r admissionWithoutBodies) Get(key []byte) ([]byte, bool, error) {
	if bytes.HasPrefix(key, observationRecordPrefix) || r.forbidden[string(key)] {
		return nil, false, errors.New("admission attempted a source-body read")
	}
	return r.Reader.Get(key)
}

func TestSourceAdmissionAuthenticatesRawReceiptBindingAndPresence(t *testing.T) {
	for _, damage := range []string{"none", "wrong record", "missing primary", "missing receipt", "legacy unknown time"} {
		t.Run(damage, func(t *testing.T) {
			ctx := context.Background()
			s, _, _ := rollupTestStore(t, 0xdb)
			at := time.Date(2010, 3, 2, 10, 0, 0, 0, time.UTC)
			id := rollupTestSource(t, s, "Saved source for receipt admission.", at)
			primary := s.logicalKey(groupRecord, kindObservation, "", id)
			order := s.logicalKey(groupOrder, kindObservation, "", id)
			defer clear(primary)
			defer clear(order)
			if damage != "none" {
				if err := s.Update(ctx, func(tx *Tx) error {
					switch damage {
					case "missing primary":
						return tx.writer.Delete(primary)
					case "missing receipt":
						return tx.writer.Delete(order)
					case "wrong record":
						other := s.logicalKey(groupRecord, kindObservation, "", strings.Repeat("f", 64))
						defer clear(other)
						raw, err := s.encodeSizeEntry(order, other, 50, at)
						if err != nil {
							return err
						}
						defer clear(raw)
						return tx.writer.Put(order, raw)
					case "legacy unknown time":
						plain := make([]byte, legacySizeRecordBytes)
						defer clear(plain)
						plain[0] = 1
						binary.BigEndian.PutUint32(plain[1:5], 50)
						copy(plain[5:], primary)
						raw, err := s.seal(order, "observation-size", plain)
						if err != nil {
							return err
						}
						defer clear(raw)
						return tx.writer.Put(order, raw)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			err := s.view(ctx, func(reader bolt.Reader) error {
				first, found, err := s.sourceAdmissionReader(admissionWithoutBodies{Reader: reader}, id)
				if damage == "none" || damage == "legacy unknown time" {
					if err != nil || !found {
						t.Fatalf("valid source rejected: %v %v", found, err)
					}
					if damage == "none" && !first.Equal(at) || damage == "legacy unknown time" && !first.IsZero() {
						t.Fatalf("capture time changed: %v", first)
					}
				} else if !errors.Is(err, ErrCorrupt) || found {
					t.Fatalf("damaged receipt admitted: %v %v", found, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSourceAdmissionKeepsPublishedMembershipDuringTokenMigrationAndErasure(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xdc)
	at := time.Date(2010, 3, 2, 10, 0, 0, 0, time.UTC)
	summary := publishIndexedFixture(t, s, strings.Repeat("José reviewed the release evidence.\n", 2200), at)
	drainCaptureIndex(t, s)
	ids, _, err := s.ListCaptureSourceIDs(ctx, summary.ContentID(), -1, 64)
	if err != nil || len(ids) < 2 {
		t.Fatalf("parts fixture: %v %v", ids, err)
	}
	previousCaptureTokens(t, s, summary, true)
	if err := s.view(ctx, func(reader bolt.Reader) error {
		edition, _, _, err := s.captureEditionReader(reader, summary.ContentID())
		if err != nil {
			return err
		}
		forbidden := map[string]bool{}
		for i := int64(0); i < summary.PartCount(); i++ {
			key := s.logicalKey(groupRecord, kindIndex, "index:"+captureNS, capturePartKey(edition.Owner, i))
			forbidden[string(key)] = true
			clear(key)
		}
		for _, id := range ids {
			first, found, err := s.sourceAdmissionReader(admissionWithoutBodies{Reader: reader, forbidden: forbidden}, id)
			if err != nil || !found || !first.Equal(at) {
				t.Fatalf("valid migrating part rejected: %s %v %v %v", id, first, found, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteCapturesBefore(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.view(ctx, func(reader bolt.Reader) error {
		for _, id := range ids {
			_, found, err := s.sourceAdmissionReader(reader, id)
			if err != nil || found {
				t.Fatalf("erased part still admitted: %v %v", found, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
