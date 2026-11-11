package securestore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/hazcod/blur/internal/platform"
)

type secretBackend struct {
	mu         sync.Mutex
	value      []byte
	err        error
	gets, puts int
	putErr     error
	onPut      func(*secretBackend, []byte)
}

func (*secretBackend) Capability(context.Context) (platform.CapabilityReport, error) {
	return platform.CapabilityReport{}, nil
}
func (b *secretBackend) Get(context.Context, string, int) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gets++
	return append([]byte(nil), b.value...), b.err
}
func (b *secretBackend) Put(_ context.Context, _ string, value []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.puts++
	if b.putErr != nil {
		return b.putErr
	}
	b.value = append([]byte(nil), value...)
	b.err = nil
	if b.onPut != nil {
		b.onPut(b, value)
	}
	return nil
}
func (*secretBackend) Delete(context.Context, string) error { return nil }

type failReader struct{}

func (failReader) Read(p []byte) (int, error) { p[0] = 7; return 1, io.ErrUnexpectedEOF }

func TestOnlyExplicitNotFoundCreatesKey(t *testing.T) {
	for _, state := range []SecretState{Locked, Corrupt, Denied, Unavailable, Ambiguous} {
		t.Run(string(state), func(t *testing.T) {
			b := &secretBackend{err: &SecretError{State: state}}
			_, err := ResolveKey(context.Background(), b, "database", bytes.NewReader(bytes.Repeat([]byte{9}, 32)))
			var se *SecretError
			if !errors.As(err, &se) || se.State != state || b.puts != 0 {
				t.Fatalf("state=%v puts=%d", err, b.puts)
			}
		})
	}
	for _, tc := range []struct {
		name  string
		value []byte
		err   error
	}{
		{"nil result", nil, nil}, {"unknown state", nil, &SecretError{State: "mystery"}},
		{"backend error", nil, errors.New("PRIVATE BACKEND DETAIL")},
		{"wrapped not found", nil, errors.Join(errors.New("PRIVATE BACKEND DETAIL"), &SecretError{State: NotFound})},
		{"typed nil error", nil, (*SecretError)(nil)},
		{"wrong size", []byte{1}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &secretBackend{value: tc.value, err: tc.err}
			_, err := ResolveKey(context.Background(), b, "database", failReader{})
			if err == nil || b.puts != 0 || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("error=%v puts=%d", err, b.puts)
			}
		})
	}
	b := &secretBackend{err: &SecretError{State: NotFound}}
	key, err := ResolveKey(context.Background(), b, "database", bytes.NewReader(bytes.Repeat([]byte{3}, 32)))
	if err != nil || key.Bytes() != [32]byte(bytes.Repeat([]byte{3}, 32)) || b.puts != 1 {
		t.Fatalf("key match=%t err=%v puts=%d", key.Bytes() == [32]byte(bytes.Repeat([]byte{3}, 32)), err, b.puts)
	}
}

func TestLoadKeyNeverCreatesWhenMissing(t *testing.T) {
	b := &secretBackend{err: &SecretError{State: NotFound}}
	_, err := LoadKey(context.Background(), b, "database")
	var secretErr *SecretError
	if !errors.As(err, &secretErr) || secretErr.State != NotFound || b.gets != 1 || b.puts != 0 {
		t.Fatalf("LoadKey() error=%v gets=%d puts=%d", err, b.gets, b.puts)
	}
}

func TestLoadKeyReturnsExistingKeyWithoutMutation(t *testing.T) {
	want := bytes.Repeat([]byte{0x5a}, 32)
	b := &secretBackend{value: append([]byte(nil), want...)}
	key, err := LoadKey(context.Background(), b, "database")
	if err != nil || key.Bytes() != [32]byte(want) || b.puts != 0 {
		t.Fatalf("LoadKey() match=%t error=%v puts=%d", key.Bytes() == [32]byte(want), err, b.puts)
	}
}

func TestRandomAndPersistenceFailuresFailClosed(t *testing.T) {
	b := &secretBackend{err: &SecretError{State: NotFound}}
	if _, err := ResolveKey(context.Background(), b, "database", failReader{}); err == nil || b.puts != 0 {
		t.Fatalf("err=%v puts=%d", err, b.puts)
	}
	b.putErr = errors.New("PRIVATE PUT DETAIL")
	_, err := ResolveKey(context.Background(), b, "database", bytes.NewReader(bytes.Repeat([]byte{4}, 32)))
	if err == nil || b.puts != 1 || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("err=%v puts=%d", err, b.puts)
	}
	b = &secretBackend{err: &SecretError{State: NotFound}, putErr: &SecretError{State: Denied}}
	_, err = ResolveKey(context.Background(), b, "database", bytes.NewReader(bytes.Repeat([]byte{4}, 32)))
	var secretErr *SecretError
	if !errors.As(err, &secretErr) || secretErr.State != Denied || secretErr.Operation != "create" {
		t.Fatalf("typed Put failure = %v, want denied during create", err)
	}
}

func TestResolveKeyPreservesNumericReadStatusOnly(t *testing.T) {
	b := &secretBackend{err: &SecretError{State: Unavailable, BackendCode: -50}}
	_, err := ResolveKey(context.Background(), b, "database", failReader{})
	var secretErr *SecretError
	if !errors.As(err, &secretErr) || secretErr.State != Unavailable || secretErr.Operation != "read" || secretErr.BackendCode != -50 {
		t.Fatalf("read failure = %#v, want only numeric status and operation", err)
	}
	if strings.Contains(err.Error(), "-50") {
		t.Fatalf("formatted secret-store error exposed backend code: %v", err)
	}
}

func TestCancelledResolutionDoesNotReadOrWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := &secretBackend{err: &SecretError{State: NotFound}}
	_, err := ResolveKey(ctx, b, "database", failReader{})
	if !errors.Is(err, context.Canceled) || b.gets != 0 || b.puts != 0 {
		t.Fatalf("err=%v gets=%d puts=%d", err, b.gets, b.puts)
	}
}

func TestConcurrentResolutionCreatesOneKeyAndCopiesFoundValue(t *testing.T) {
	b := &secretBackend{err: &SecretError{State: NotFound}}
	random := bytes.NewReader(bytes.Repeat([]byte{5}, 32))
	var wg sync.WaitGroup
	keys := make([]Key, 20)
	errs := make([]error, 20)
	for i := range keys {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			keys[i], errs[i] = ResolveKey(context.Background(), b, "database", random)
		}(i)
	}
	wg.Wait()
	for i := range keys {
		if errs[i] != nil || !keys[i].Equal(keys[0]) {
			t.Fatalf("index %d: err=%v", i, errs[i])
		}
	}
	if b.puts != 1 {
		t.Fatalf("puts=%d", b.puts)
	}
	b.value[0] = 8
	if keys[0].Bytes()[0] != 5 {
		t.Fatal("returned key aliases backend")
	}
}

func TestKeyDoesNotFormatOrSerializeAndBytesAreCopies(t *testing.T) {
	b := &secretBackend{value: bytes.Repeat([]byte{7}, 32)}
	key, err := ResolveKey(context.Background(), b, "database", failReader{})
	if err != nil {
		t.Fatal(err)
	}
	formatted := fmt.Sprintf("%v %+v %#v %s", key, key, key, key)
	if strings.Contains(formatted, "7 7") || strings.Contains(formatted, "[7") {
		t.Fatalf("key leaked through formatting")
	}
	if _, err := json.Marshal(key); err == nil {
		t.Fatal("JSON serialized key")
	}
	if _, err := key.MarshalText(); err == nil {
		t.Fatal("text serialized key")
	}
	copy := key.Bytes()
	copy[0] = 9
	if key.Bytes()[0] != 7 {
		t.Fatal("byte accessor aliased key")
	}
}

func TestPostPutVerificationRejectsMutationAndAmbiguousReread(t *testing.T) {
	for _, tc := range []struct {
		name  string
		onPut func(*secretBackend, []byte)
	}{
		{"mutated input only", func(_ *secretBackend, v []byte) { v[0] = 8 }},
		{"mutated input", func(b *secretBackend, v []byte) { v[0] = 8; b.value = append([]byte(nil), v...) }},
		{"wrong length", func(b *secretBackend, _ []byte) { b.value = []byte{1} }},
		{"locked", func(b *secretBackend, _ []byte) { b.err = &SecretError{State: Locked} }},
		{"missing", func(b *secretBackend, _ []byte) { b.err = &SecretError{State: NotFound}; b.value = nil }},
		{"backend error", func(b *secretBackend, _ []byte) { b.err = errors.New("PRIVATE REREAD DETAIL") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &secretBackend{err: &SecretError{State: NotFound}, onPut: tc.onPut}
			key, err := ResolveKey(context.Background(), b, "database", bytes.NewReader(bytes.Repeat([]byte{3}, 32)))
			if err == nil || key.Bytes() != [32]byte{} || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("verification err=%v", err)
			}
		})
	}
}

func TestTemporaryPutBufferIsCleared(t *testing.T) {
	var putBuffer []byte
	b := &secretBackend{err: &SecretError{State: NotFound}, onPut: func(_ *secretBackend, v []byte) { putBuffer = v }}
	if _, err := ResolveKey(context.Background(), b, "database", bytes.NewReader(bytes.Repeat([]byte{3}, 32))); err != nil {
		t.Fatal(err)
	}
	if len(putBuffer) != 32 {
		t.Fatal("missing temporary buffer")
	}
	for _, v := range putBuffer {
		if v != 0 {
			t.Fatal("temporary buffer retained key")
		}
	}
}

func TestPostPutCancellationReturnsNoKey(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := &secretBackend{err: &SecretError{State: NotFound}, onPut: func(_ *secretBackend, _ []byte) { cancel() }}
	key, err := ResolveKey(ctx, b, "database", bytes.NewReader(bytes.Repeat([]byte{3}, 32)))
	if !errors.Is(err, context.Canceled) || key.Bytes() != [32]byte{} {
		t.Fatalf("cancel err=%v", err)
	}
}
