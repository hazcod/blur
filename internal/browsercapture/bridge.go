package browsercapture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/storage"
)

const MaxWireBytes = 288 << 10
const maxJSONDepth = 16

var (
	ErrReceiverPaused = errors.New("browser capture: receiver paused")
	errInvalidFrame   = errors.New("browser capture: invalid frame")
)

type ObservationSink interface {
	EncryptedAtRest() bool
	PutObservation(context.Context, capture.SanitizedObservation) error
}

// CommitPolicy validates the latest user scope and invokes write while that
// policy is stable. Implementations must honor ctx and must not retain write.
// A rejection must return without invoking write.
type CommitPolicy func(context.Context, capture.SanitizedObservation, func(context.Context) error) error

type Receiver struct {
	mu            sync.Mutex
	cond          *sync.Cond
	grants        *localapi.GrantStore
	sink          ObservationSink
	enabled       func() bool
	clock         func() time.Time
	paused        bool
	inFlight      int
	policy        CommitPolicy
	documentsMu   sync.Mutex
	documents     map[string]*documentJob
	onObservation func(capture.SanitizedObservation, time.Time)
	onDocument    func(capture.CaptureSummary, bool)

	droppedShort atomic.Uint64
}

// DroppedShort counts snapshots discarded for carrying fewer than
// capture.MinObservationChars non-whitespace characters.
func (r *Receiver) DroppedShort() uint64 { return r.droppedShort.Load() }

func NewReceiver(grants *localapi.GrantStore, sink ObservationSink, enabled func() bool, clock func() time.Time, policies ...CommitPolicy) (*Receiver, error) {
	if grants == nil || sink == nil || !sink.EncryptedAtRest() || enabled == nil {
		return nil, errInvalidFrame
	}
	if clock == nil {
		clock = time.Now
	}
	if len(policies) > 1 || (len(policies) == 1 && policies[0] == nil) {
		return nil, errInvalidFrame
	}
	r := &Receiver{grants: grants, sink: sink, enabled: enabled, clock: clock}
	if len(policies) == 1 {
		r.policy = policies[0]
	}
	r.cond = sync.NewCond(&r.mu)
	return r, nil
}

type bridgeEnvelope struct {
	Version int             `json:"version"`
	Payload json.RawMessage `json:"payload"`
	Proof   bridgeProof     `json:"proof"`
}
type bridgeProof struct {
	GrantID   string    `json:"grant_id"`
	ClientID  string    `json:"client_id"`
	Scope     string    `json:"scope"`
	Timestamp time.Time `json:"timestamp"`
	Nonce     string    `json:"nonce"`
	MAC       string    `json:"mac"`
}

func (r *Receiver) Accept(ctx context.Context, wire []byte) error {
	if r == nil || ctx == nil || ctx.Err() != nil || len(wire) == 0 || len(wire) > MaxWireBytes || !utf8.Valid(wire) {
		return errInvalidFrame
	}
	r.mu.Lock()
	if r.paused {
		r.mu.Unlock()
		return ErrReceiverPaused
	}
	if !r.enabled() {
		r.mu.Unlock()
		return ErrReceiverPaused
	}
	r.inFlight++
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.inFlight--
		if r.inFlight == 0 {
			r.cond.Broadcast()
		}
		r.mu.Unlock()
	}()

	var envelope bridgeEnvelope
	if err := decodeStrict(wire, &envelope); err != nil || (envelope.Version != 1 && envelope.Version != 2) || len(envelope.Payload) == 0 || len(envelope.Payload) > MaxPageBytes+MaxTitleBytes+MaxURLBytes+4096 {
		return errInvalidFrame
	}
	var page Snapshot
	if err := decodeStrict(envelope.Payload, &page); err != nil {
		return errInvalidFrame
	}
	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Proof.Nonce)
	if err != nil || len(nonce) != localapi.NonceBytes {
		return errInvalidFrame
	}
	macBytes, err := hex.DecodeString(envelope.Proof.MAC)
	if err != nil || len(macBytes) != 32 {
		return errInvalidFrame
	}
	var mac [32]byte
	copy(mac[:], macBytes)
	proof := localapi.Proof{GrantID: envelope.Proof.GrantID, ClientID: envelope.Proof.ClientID, Scope: localapi.Scope(envelope.Proof.Scope), Timestamp: envelope.Proof.Timestamp, Nonce: nonce, MAC: mac}
	digest := sha256.Sum256(envelope.Payload)
	if err := r.grants.Authorize(proof, digest); err != nil {
		return errInvalidFrame
	}
	observedAt := r.clock()
	if envelope.Version == 2 {
		selector, err := validateDocumentSnapshot(page, observedAt)
		if err != nil {
			return err
		}
		var work documentWork
		write := func(writeCtx context.Context) error {
			var err error
			work, err = r.persistDocument(writeCtx, page, selector, observedAt)
			return err
		}
		if r.policy != nil {
			err = r.policy(ctx, selector, write)
		} else {
			err = write(ctx)
		}
		r.finishDocumentWork(work)
		return err
	}
	observation, err := Sanitize(page, true, observedAt)
	if err != nil {
		return ErrRejected
	}
	committed := false
	write := func(writeCtx context.Context) error {
		var err error
		committed, err = r.persistObservation(writeCtx, observation, observedAt)
		return err
	}
	if r.policy != nil {
		err = r.policy(ctx, observation, write)
	} else {
		err = write(ctx)
	}
	if committed {
		r.notifyObservation(observation, observedAt)
	}
	return err
}

func (r *Receiver) persist(ctx context.Context, observation capture.SanitizedObservation, observedAt time.Time) error {
	_, err := r.persistObservation(ctx, observation, observedAt)
	return err
}

func (r *Receiver) persistObservation(ctx context.Context, observation capture.SanitizedObservation, observedAt time.Time) (bool, error) {
	if ctx == nil {
		return false, ErrRejected
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.Lock()
	allowed := !r.paused && r.enabled()
	r.mu.Unlock()
	if !allowed {
		return false, ErrReceiverPaused
	}
	if !capture.MeetsMinimumContent(observation.AppID(), observation.Text()) {
		r.droppedShort.Add(1)
		return false, nil
	}
	var err error
	if sink, ok := r.sink.(interface {
		PutObservationAt(context.Context, capture.SanitizedObservation, time.Time) error
	}); ok {
		err = sink.PutObservationAt(ctx, observation, observedAt)
	} else {
		err = r.sink.PutObservation(ctx, observation)
	}
	var storeErr *storage.StoreError
	if errors.As(err, &storeErr) && storeErr.Code == storage.Duplicate {
		if recorder, ok := r.sink.(interface {
			RecordObservationSighting(context.Context, capture.SanitizedObservation, time.Time) error
		}); ok {
			err = recorder.RecordObservationSighting(ctx, observation, observedAt)
		}
	}
	return err == nil, err
}

func (r *Receiver) Pause() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.paused = true
	for r.inFlight > 0 {
		r.cond.Wait()
	}
	r.mu.Unlock()
	r.closeDocuments()
}

// Configure notifications before starting the server. Notifications run after
// policy admission has returned and only for committed new content or visits.
func (r *Receiver) SetNotifications(observation func(capture.SanitizedObservation, time.Time), document func(capture.CaptureSummary, bool)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight == 0 && !r.paused {
		r.onObservation, r.onDocument = observation, document
	}
}

func (r *Receiver) notifyObservation(observation capture.SanitizedObservation, at time.Time) {
	r.mu.Lock()
	notify := r.onObservation
	r.mu.Unlock()
	if notify != nil {
		notify(observation, at)
	}
}

func decodeStrict(data []byte, out any) error {
	if !utf8.Valid(data) || hasDuplicateOrDeepJSON(data) {
		return errInvalidFrame
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errInvalidFrame
	}
	return nil
}

// Token-walk the whole JSON tree before struct decoding: encoding/json alone
// accepts duplicate keys, which can make signed and interpreted fields differ.
func hasDuplicateOrDeepJSON(data []byte) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value func(int) (bool, error)
	value = func(depth int) (bool, error) {
		if depth > maxJSONDepth {
			return true, errInvalidFrame
		}
		tok, err := d.Token()
		if err != nil {
			return false, err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return false, nil
		}
		switch delim {
		case '{':
			keys := make(map[string]struct{})
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return false, err
				}
				key, ok := k.(string)
				if !ok {
					return false, errInvalidFrame
				}
				if _, exists := keys[key]; exists {
					return true, nil
				}
				keys[key] = struct{}{}
				dup, err := value(depth + 1)
				if err != nil || dup {
					return dup, err
				}
			}
			end, err := d.Token()
			return false, checkEnd(end, err, '}')
		case '[':
			for d.More() {
				dup, err := value(depth + 1)
				if err != nil || dup {
					return dup, err
				}
			}
			end, err := d.Token()
			return false, checkEnd(end, err, ']')
		default:
			return false, errInvalidFrame
		}
	}
	dup, err := value(0)
	if err != nil || dup {
		return true
	}
	_, err = d.Token()
	return err != io.EOF
}
func checkEnd(tok json.Token, err error, want rune) error {
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok || rune(d) != want {
		return errInvalidFrame
	}
	return nil
}
