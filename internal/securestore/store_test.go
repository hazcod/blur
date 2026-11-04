package securestore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/nielsgroen/blur/internal/platform"
)

type secretBackend struct {
	mu         sync.Mutex
	value      []byte
	err        error
	gets, puts int
	putErr     error
}

func (*secretBackend) Capability(context.Context) (platform.CapabilityReport, error) {
	return platform.CapabilityReport{}, nil
}
func (b *secretBackend) Get(context.Context, string, int) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gets++
	return b.value, b.err
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
	return nil
}
func (*secretBackend) Delete(context.Context, string) error { return nil }

type failReader struct{}

func (failReader) Read(p []byte) (int, error) { p[0] = 7; return 1, io.ErrUnexpectedEOF }

func TestOnlyExplicitNotFoundCreatesKey(t *testing.T) {
	for _, state := range []SecretState{Locked, Corrupt, Denied, Unavailable} {
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
	if err != nil || key != [32]byte(bytes.Repeat([]byte{3}, 32)) || b.puts != 1 {
		t.Fatalf("key match=%t err=%v puts=%d", key == [32]byte(bytes.Repeat([]byte{3}, 32)), err, b.puts)
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
	keys := make([][32]byte, 20)
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
		if errs[i] != nil || keys[i] != keys[0] {
			t.Fatalf("index %d: err=%v", i, errs[i])
		}
	}
	if b.puts != 1 {
		t.Fatalf("puts=%d", b.puts)
	}
	b.value[0] = 8
	if keys[0][0] != 5 {
		t.Fatal("returned key aliases backend")
	}
}
