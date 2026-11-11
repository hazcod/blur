package linux

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/hazcod/blur/internal/platform"
)

type fakeSecretBackend struct {
	values map[string][]byte
	state  platform.SecretState
	calls  int
	block  bool
}

func (f *fakeSecretBackend) capability(context.Context) (platform.CapabilityReport, error) {
	return platform.CapabilityReport{ID: "secret_store", State: platform.Available, Evidence: "secret_service"}, nil
}
func (f *fakeSecretBackend) get(ctx context.Context, name string, _ int) ([]byte, error) {
	f.calls++
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.state != "" {
		return nil, &platform.SecretStoreError{State: f.state}
	}
	return append([]byte(nil), f.values[name]...), nil
}

func TestSecretStoreAppliesAnOperationDeadlineAndKeepsTimeoutTyped(t *testing.T) {
	store := newSecretStore(&fakeSecretBackend{block: true})
	store.operationTimeout = 15 * time.Millisecond
	_, err := store.Get(context.Background(), "database", 32)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unbounded D-Bus operation error = %v", err)
	}
}
func (f *fakeSecretBackend) put(_ context.Context, name string, value []byte) error {
	f.calls++
	if f.state != "" {
		return &platform.SecretStoreError{State: f.state}
	}
	if f.values == nil {
		f.values = make(map[string][]byte)
	}
	f.values[name] = append([]byte(nil), value...)
	return nil
}
func (f *fakeSecretBackend) delete(_ context.Context, name string) error {
	f.calls++
	if f.state != "" {
		return &platform.SecretStoreError{State: f.state}
	}
	delete(f.values, name)
	return nil
}

func TestSecretStoreValidatesCopiesAndPropagatesLockedState(t *testing.T) {
	backend := &fakeSecretBackend{}
	store := newSecretStore(backend)
	value := []byte{1, 2, 3, 4}
	if err := store.Put(context.Background(), "database", value); err != nil {
		t.Fatal(err)
	}
	value[0] = 9
	got, err := store.Get(context.Background(), "database", 4)
	if err != nil || !bytes.Equal(got, []byte{1, 2, 3, 4}) {
		t.Fatalf("Get = %v, %v", got, err)
	}
	got[0] = 8
	if !bytes.Equal(backend.values["database"], []byte{1, 2, 3, 4}) {
		t.Fatal("caller mutation changed backend secret")
	}
	if _, err := store.Get(context.Background(), "../database", 4); !secretStateIs(err, platform.SecretUnavailable) {
		t.Fatalf("invalid name error = %v", err)
	}
	if backend.calls != 2 {
		t.Fatalf("invalid request reached backend: calls=%d", backend.calls)
	}
	backend.state = platform.SecretLocked
	if _, err := store.Get(context.Background(), "database", 4); !secretStateIs(err, platform.SecretLocked) {
		t.Fatalf("locked item state lost: %v", err)
	}
}

func TestSecretTransferCipherRoundTripAndContextCancellation(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 16)
	plain := []byte("blur database key material")
	iv, ciphertext, err := encryptSecretTransfer(key, plain)
	if err != nil || bytes.Contains(ciphertext, plain) {
		t.Fatalf("encrypted transfer leaked plaintext: %x err=%v", ciphertext, err)
	}
	opened, err := decryptSecretTransfer(key, iv, ciphertext)
	if err != nil || !bytes.Equal(opened, plain) {
		t.Fatalf("decrypt = %q, %v", opened, err)
	}
	clear(opened)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newSecretStore(&fakeSecretBackend{}).Get(ctx, "database", 32); err != context.Canceled {
		t.Fatalf("canceled Get = %v", err)
	}
}

func TestSecretServiceDHAgreementUses1024BitGroupAndRejectsInvalidPeer(t *testing.T) {
	prime, ok := new(big.Int).SetString(secretDHPrimeHex, 16)
	if !ok || prime.BitLen() != 1024 {
		t.Fatalf("Secret Service DH group has %d bits", prime.BitLen())
	}
	generator := big.NewInt(2)
	privateA, privateB := big.NewInt(12345), big.NewInt(67890)
	publicA := new(big.Int).Exp(generator, privateA, prime)
	publicB := new(big.Int).Exp(generator, privateB, prime)
	keyA, err := deriveSecretSessionKey(privateA, publicB, prime)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(keyA)
	keyB, err := deriveSecretSessionKey(privateB, publicA, prime)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(keyB)
	if !bytes.Equal(keyA, keyB) || len(keyA) != 16 {
		t.Fatal("DH peers did not derive the same AES-128 key")
	}
	invalidPeer := new(big.Int).Sub(prime, big.NewInt(1))
	if _, err := deriveSecretSessionKey(privateA, invalidPeer, prime); err == nil {
		t.Fatal("out-of-range peer public key accepted")
	}
}

func TestSecretMatchSelectionNeverChoosesAmongAmbiguousOrLockedItems(t *testing.T) {
	first := dbus.ObjectPath("/org/freedesktop/secrets/item/1")
	second := dbus.ObjectPath("/org/freedesktop/secrets/item/2")
	for _, tc := range []struct {
		name     string
		unlocked []dbus.ObjectPath
		locked   []dbus.ObjectPath
		want     platform.SecretState
	}{{"unique", []dbus.ObjectPath{first}, nil, platform.SecretFound}, {"locked", nil, []dbus.ObjectPath{first}, platform.SecretLocked}, {"duplicate", []dbus.ObjectPath{first, second}, nil, platform.SecretAmbiguous}, {"locked duplicate", []dbus.ObjectPath{first}, []dbus.ObjectPath{second}, platform.SecretAmbiguous}, {"missing", nil, nil, platform.SecretNotFound}} {
		t.Run(tc.name, func(t *testing.T) {
			_, state := selectSecretItem(tc.unlocked, tc.locked)
			if state != tc.want {
				t.Fatalf("selected state %q, want %q", state, tc.want)
			}
		})
	}
}

func secretStateIs(err error, want platform.SecretState) bool {
	secret, ok := err.(*platform.SecretStoreError)
	return ok && secret.State == want
}
