package durable

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// Question history is an additive record family: opening a store written
// before it existed needs no migration and the schema version is unchanged.
// Record IDs and namespaces are HMAC-keyed like every other index record.
//
//	question-history      one record per question, keyed by sequence number
//	question-history-ix   the ordered sequence list and the next sequence
//	question-history-src  cited source ID -> sequence numbers citing it
//
// The reverse index is the deletion fence: erasing a source removes it from
// every question that cited it. The history is device-local and not replicated.
const (
	questionNS       = "question-history"
	questionIndexNS  = "question-history-ix"
	questionSourceNS = "question-history-src"
	questionIndexID  = "order"

	maxQuestionRecordBytes = 8 << 10
	maxQuestionIndexBytes  = 32 << 10
	maxQuestionSourceBytes = 16 << 10
	// questionSourceDropBatch bounds the questions one fence step rewrites.
	questionSourceDropBatch = 8
	// questionClearBatch bounds the questions one delete-all step removes.
	questionClearBatch = 32
)

type questionIndex struct {
	Next uint64   `json:"next"`
	IDs  []uint64 `json:"ids"` // ascending
}

type questionRow struct {
	Question string   `json:"q"`
	AskedAt  int64    `json:"at"`
	PlanKind string   `json:"kind"`
	Sources  []string `json:"src,omitempty"`
}

func questionID(seq uint64) string { return fmt.Sprintf("%020d", seq) }

func validQuestionKind(kind string) bool {
	if kind == "" || len(kind) > storage.MaxQuestionPlanKind {
		return false
	}
	for i := 0; i < len(kind); i++ {
		if c := kind[i]; (c < 'a' || c > 'z') && c != '_' {
			return false
		}
	}
	return true
}

func validQuestionRow(row questionRow) bool {
	if !storage.ValidQuestionText(row.Question) || !validQuestionKind(row.PlanKind) || !validCaptureTime(time.Unix(0, row.AskedAt).UTC()) || len(row.Sources) > storage.MaxQuestionSources {
		return false
	}
	for i, id := range row.Sources {
		if !validHash(id) || i > 0 && row.Sources[i-1] >= id {
			return false
		}
	}
	return utf8.ValidString(row.Question)
}

// AppendQuestion stores one Ask question. Cited IDs that do not resolve to a
// live source are dropped, in the same transaction as the write, so a late
// answer cannot re-link a source that was just erased.
func (s *Store) AppendQuestion(ctx context.Context, record storage.QuestionRecord) error {
	_, _, err := s.appendQuestion(ctx, record, nil, false)
	return err
}

func (s *Store) AppendQuestionAtRead(ctx context.Context, record storage.QuestionRecord, epoch uint64) (uint64, bool, error) {
	return s.appendQuestion(ctx, record, &epoch, false)
}

func (s *Store) AppendQuestionAtVoiceRead(ctx context.Context, record storage.QuestionRecord, epoch uint64) (uint64, bool, error) {
	return s.appendQuestion(ctx, record, &epoch, true)
}

func (s *Store) appendQuestion(ctx context.Context, record storage.QuestionRecord, expected *uint64, voice bool) (uint64, bool, error) {
	if ctx == nil || !storage.ValidQuestionText(record.Question) || !validQuestionKind(record.PlanKind) || !validCaptureTime(record.AskedAt) {
		return 0, false, &storage.StoreError{Code: storage.Invalid}
	}
	cited := make([]string, 0, len(record.SourceIDs))
	for _, id := range record.SourceIDs {
		if validHash(id) && !slices.Contains(cited, id) {
			cited = append(cited, id)
		}
	}
	sort.Strings(cited)
	if len(cited) > storage.MaxQuestionSources {
		cited = cited[:storage.MaxQuestionSources]
	}
	var next uint64
	var accepted bool
	err := s.Update(ctx, func(tx *Tx) error {
		// Update owns publicationMu through commit and epoch advancement.
		current := s.state.publicationEpoch
		if voice {
			current = s.state.voicePublicationEpoch
		}
		if expected != nil && current != *expected {
			return nil
		}
		admission := &chronicleAdmissionReader{Reader: tx.writer}
		defer admission.close()
		live := cited[:0:0]
		for _, id := range cited {
			_, _, found, err := s.sourceEvidenceReader(admission, id)
			if err != nil {
				return err
			}
			if found {
				live = append(live, id)
			}
		}
		admission.close()
		if err := tx.appendQuestion(questionRow{Question: record.Question, AskedAt: record.AskedAt.UTC().UnixNano(), PlanKind: record.PlanKind, Sources: live}); err != nil {
			return err
		}
		next, accepted = current, true
		if !voice {
			next++
		}
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return next, accepted, nil
}

// ListQuestions returns up to limit of the newest questions, oldest first.
func (s *Store) ListQuestions(ctx context.Context, limit int) ([]storage.QuestionRecord, error) {
	if ctx == nil || limit < 1 || limit > storage.MaxQuestionHistory {
		return nil, &storage.StoreError{Code: storage.Invalid}
	}
	var out []storage.QuestionRecord
	err := s.view(ctx, func(reader bolt.Reader) error {
		encoded, found, err := s.readIndexValueReader(reader, questionIndexNS, questionIndexID, maxQuestionIndexBytes)
		if err != nil || !found {
			return err
		}
		index, err := decodeQuestionIndex(encoded)
		clear(encoded)
		if err != nil {
			return err
		}
		ids := index.IDs
		if len(ids) > limit {
			ids = ids[len(ids)-limit:]
		}
		out = make([]storage.QuestionRecord, 0, len(ids))
		for _, seq := range ids {
			encoded, found, err := s.readIndexValueReader(reader, questionNS, questionID(seq), maxQuestionRecordBytes)
			if err != nil {
				return err
			}
			if !found {
				return ErrCorrupt
			}
			row, err := decodeQuestionRow(encoded)
			clear(encoded)
			if err != nil {
				return err
			}
			out = append(out, storage.QuestionRecord{Question: row.Question, AskedAt: time.Unix(0, row.AskedAt).UTC(), PlanKind: row.PlanKind, SourceIDs: slices.Clone(row.Sources)})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteAllQuestions clears the history and its reverse index.
func (s *Store) DeleteAllQuestions(ctx context.Context) error {
	if ctx == nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		done := false
		err := s.Update(ctx, func(tx *Tx) error {
			var err error
			done, err = tx.clearQuestionBatch()
			return err
		})
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

func decodeQuestionIndex(b []byte) (questionIndex, error) {
	var index questionIndex
	if json.Unmarshal(b, &index) != nil || len(index.IDs) > storage.MaxQuestionHistory+1 || !slices.IsSorted(index.IDs) {
		return questionIndex{}, ErrCorrupt
	}
	for i, id := range index.IDs {
		if id >= index.Next || i > 0 && index.IDs[i-1] == id {
			return questionIndex{}, ErrCorrupt
		}
	}
	return index, nil
}

func decodeQuestionRow(b []byte) (questionRow, error) {
	var row questionRow
	if json.Unmarshal(b, &row) != nil || !validQuestionRow(row) {
		return questionRow{}, ErrCorrupt
	}
	return row, nil
}

func (tx *Tx) readQuestionIndex() (questionIndex, bool, error) {
	b, found, err := tx.readIndexValue(questionIndexNS, questionIndexID, maxQuestionIndexBytes)
	if err != nil || !found {
		return questionIndex{}, found, err
	}
	defer clear(b)
	index, err := decodeQuestionIndex(b)
	return index, err == nil, err
}

func (tx *Tx) writeQuestionIndex(index questionIndex) error {
	return tx.PutIndexRecord(questionIndexNS, questionIndexID, mustJSON(index))
}

func (tx *Tx) readQuestionRow(seq uint64) (questionRow, bool, error) {
	b, found, err := tx.readIndexValue(questionNS, questionID(seq), maxQuestionRecordBytes)
	if err != nil || !found {
		return questionRow{}, found, err
	}
	defer clear(b)
	row, err := decodeQuestionRow(b)
	return row, err == nil, err
}

func (tx *Tx) writeQuestionRow(seq uint64, row questionRow) error {
	if !validQuestionRow(row) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	encoded := mustJSON(row)
	defer clear(encoded)
	return tx.PutIndexRecord(questionNS, questionID(seq), encoded)
}

func (tx *Tx) readQuestionSource(source string) ([]uint64, bool, error) {
	b, found, err := tx.readIndexValue(questionSourceNS, source, maxQuestionSourceBytes)
	if err != nil || !found {
		return nil, found, err
	}
	defer clear(b)
	var seqs []uint64
	if json.Unmarshal(b, &seqs) != nil || len(seqs) == 0 || len(seqs) > storage.MaxQuestionHistory+1 || !slices.IsSorted(seqs) {
		return nil, false, ErrCorrupt
	}
	return seqs, true, nil
}

func (tx *Tx) writeQuestionSource(source string, seqs []uint64) error {
	if len(seqs) == 0 {
		_, found, err := tx.readQuestionSource(source)
		if err != nil || !found {
			return err
		}
		return tx.deleteKnowledgeRecord(questionSourceNS, source)
	}
	return tx.PutIndexRecord(questionSourceNS, source, mustJSON(seqs))
}

func (tx *Tx) linkQuestionSource(source string, seq uint64) error {
	seqs, _, err := tx.readQuestionSource(source)
	if err != nil {
		return err
	}
	i := sort.Search(len(seqs), func(i int) bool { return seqs[i] >= seq })
	if i < len(seqs) && seqs[i] == seq {
		return nil
	}
	return tx.writeQuestionSource(source, slices.Insert(seqs, i, seq))
}

func (tx *Tx) unlinkQuestionSource(source string, seq uint64) error {
	seqs, found, err := tx.readQuestionSource(source)
	if err != nil || !found {
		return err
	}
	i := sort.Search(len(seqs), func(i int) bool { return seqs[i] >= seq })
	if i >= len(seqs) || seqs[i] != seq {
		return nil
	}
	return tx.writeQuestionSource(source, slices.Delete(seqs, i, i+1))
}

func (tx *Tx) appendQuestion(row questionRow) error {
	index, _, err := tx.readQuestionIndex()
	if err != nil {
		return err
	}
	seq := index.Next
	if seq == ^uint64(0) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	if err := tx.writeQuestionRow(seq, row); err != nil {
		return err
	}
	for _, source := range row.Sources {
		if err := tx.linkQuestionSource(source, seq); err != nil {
			return err
		}
	}
	index.Next++
	index.IDs = append(index.IDs, seq)
	for len(index.IDs) > storage.MaxQuestionHistory {
		if err := tx.deleteQuestion(index.IDs[0]); err != nil {
			return err
		}
		index.IDs = index.IDs[1:]
	}
	return tx.writeQuestionIndex(index)
}

// deleteQuestion removes one row and its reverse links. The caller owns the
// order list.
func (tx *Tx) deleteQuestion(seq uint64) error {
	row, found, err := tx.readQuestionRow(seq)
	if err != nil {
		return err
	}
	if !found {
		return ErrCorrupt
	}
	for _, source := range row.Sources {
		if err := tx.unlinkQuestionSource(source, seq); err != nil {
			return err
		}
	}
	return tx.deleteKnowledgeRecord(questionNS, questionID(seq))
}

// removeQuestionHistorySourceBatch is the deletion fence. Each question that
// cited an erased source drops that ID. A bounded number is handled per call;
// it reports true once no question cites the source.
func (tx *Tx) removeQuestionHistorySourceBatch(source string) (bool, error) {
	for range questionSourceDropBatch {
		seqs, found, err := tx.readQuestionSource(source)
		if err != nil {
			return false, err
		}
		if !found {
			return true, nil
		}
		seq := seqs[0]
		row, exists, err := tx.readQuestionRow(seq)
		if err != nil {
			return false, err
		}
		if !exists {
			return false, ErrCorrupt
		}
		kept := slices.DeleteFunc(slices.Clone(row.Sources), func(id string) bool { return id == source })
		if len(kept) != len(row.Sources) {
			row.Sources = kept
			if err := tx.writeQuestionRow(seq, row); err != nil {
				return false, err
			}
		}
		if err := tx.writeQuestionSource(source, seqs[1:]); err != nil {
			return false, err
		}
	}
	_, found, err := tx.readQuestionSource(source)
	return !found, err
}

// removeQuestionHistorySource is the single-transaction form used by the
// unstaged deletion path.
func (tx *Tx) removeQuestionHistorySource(source string) error {
	complete, err := tx.removeQuestionHistorySourceBatch(source)
	if err != nil {
		return err
	}
	if !complete {
		return ErrTransactionBudget
	}
	return nil
}

func (tx *Tx) clearQuestionBatch() (bool, error) {
	index, found, err := tx.readQuestionIndex()
	if err != nil || !found {
		return true, err
	}
	n := min(questionClearBatch, len(index.IDs))
	for _, seq := range index.IDs[:n] {
		if err := tx.deleteQuestion(seq); err != nil {
			return false, err
		}
	}
	index.IDs = index.IDs[n:]
	if len(index.IDs) > 0 {
		return false, tx.writeQuestionIndex(index)
	}
	return true, tx.deleteKnowledgeRecord(questionIndexNS, questionIndexID)
}
