package darwin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"
	"unsafe"

	"github.com/hazcod/blur/internal/platform"
)

func TestSecretStoreCopiesValuesAndHonorsBounds(t *testing.T) {
	backend := &testKeychain{values: map[string][]byte{"database": []byte("secret")}}
	store := &SecretStore{backend: backend}
	got, err := store.Get(context.Background(), "database", 16)
	if err != nil || string(got) != "secret" {
		t.Fatalf("Get() = %q, %v", got, err)
	}
	got[0] = 'X'
	if string(backend.values["database"]) != "secret" {
		t.Fatal("Get returned storage-backed bytes")
	}
	if _, err := store.Get(context.Background(), "database", 5); !isSecretState(err, platform.SecretCorrupt) {
		t.Fatalf("oversized Get() error = %v", err)
	}
	if _, err := store.Get(context.Background(), "../database", 16); !isSecretState(err, platform.SecretUnavailable) {
		t.Fatalf("invalid name Get() error = %v", err)
	}
}

func TestSecretStoreMapsNotFoundAndCopiesPutInput(t *testing.T) {
	backend := &testKeychain{values: make(map[string][]byte)}
	store := &SecretStore{backend: backend}
	if _, err := store.Get(context.Background(), "database", 32); !isSecretState(err, platform.SecretNotFound) {
		t.Fatalf("missing Get() error = %v", err)
	}
	input := []byte("sensitive-key")
	if err := store.Put(context.Background(), "database", input); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	input[0] = 'X'
	if string(backend.values["database"]) != "sensitive-key" {
		t.Fatal("Put retained caller-owned bytes")
	}
	if err := store.Delete(context.Background(), "database"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Get(context.Background(), "database", 32); !isSecretState(err, platform.SecretNotFound) {
		t.Fatalf("Get after Delete() error = %v", err)
	}
}

func TestSecretStoreRejectsCanceledContextWithoutBackendCall(t *testing.T) {
	backend := &testKeychain{values: make(map[string][]byte)}
	store := &SecretStore{backend: backend}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Put(ctx, "database", []byte("secret")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put() error = %v", err)
	}
	if backend.calls != 0 {
		t.Fatalf("backend calls = %d, want 0", backend.calls)
	}
}

func TestSharedSecretStoreRequiresAnExplicitServiceAndAccessGroup(t *testing.T) {
	store, err := NewSharedSecretStore("sh.blur.safari", "TEAM123.group.sh.blur")
	if err != nil {
		t.Fatal("valid shared Keychain identity rejected")
	}
	backend, ok := store.backend.(*nativeKeychain)
	if !ok || backend.service != "sh.blur.safari" || backend.accessGroup != "TEAM123.group.sh.blur" {
		t.Fatal("shared Keychain scope was not retained")
	}
	for _, identity := range [][2]string{{"", "TEAM123.group.sh.blur"}, {"sh.blur.safari", ""}, {"sh.blur.safari", "../group"}, {"sh.blur.safari", "team bad.group"}} {
		if _, err := NewSharedSecretStore(identity[0], identity[1]); err == nil {
			t.Fatalf("accepted invalid shared Keychain identity %q", identity)
		}
	}
}

func TestKeychainStatusRetainsOnlyNumericOSCodeForDiagnostics(t *testing.T) {
	err := keychainStatus(-50)
	var storeErr *platform.SecretStoreError
	if !errors.As(err, &storeErr) || storeErr.State != platform.SecretUnavailable || storeErr.BackendCode != -50 {
		t.Fatalf("Keychain status = %#v, want unavailable with numeric status", err)
	}
	if err.Error() != "platform.secret_store: unavailable" {
		t.Fatalf("formatted Keychain error leaked backend details: %q", err)
	}
}

func TestNativeKeychainQueryReturnsNotFoundForUniqueReadOnlyProbe(t *testing.T) {
	backend := &nativeKeychain{}
	if err := backend.load(); err != nil {
		t.Fatalf("load Keychain/CoreFoundation symbols: %v", err)
	}
	if backend.api.keyClass == 0 || backend.api.keyClass == backend.api.classGeneric {
		t.Fatal("kSecClass key must be distinct from kSecClassGenericPassword value")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal("could not create a unique read-only probe")
	}
	service := "sh.blur.probe." + hex.EncodeToString(nonce[:])
	a := backend.api
	serviceRef, accountRef := a.string(service), a.string("probe")
	query, release, err := a.dictionary(
		[]uintptr{a.keyClass, a.attrService, a.attrAccount, a.attrReturnData},
		[]uintptr{a.classGeneric, serviceRef, accountRef, a.trueValue},
	)
	clear(nonce[:])
	if err != nil {
		t.Fatal("could not construct a read-only Keychain probe")
	}
	defer release()
	if count := a.cfDictionaryGetCount(query); count != 4 {
		t.Fatalf("read-only Keychain query has %d attributes; want 4", count)
	}
	for _, pair := range [][2]uintptr{{a.keyClass, a.classGeneric}, {a.attrService, serviceRef}, {a.attrAccount, accountRef}, {a.attrReturnData, a.trueValue}} {
		if got := a.cfDictionaryGetValue(query, pair[0]); got == 0 || got != pair[1] {
			t.Fatal("read-only Keychain query contains an invalid typed attribute")
		}
	}
	var result uintptr
	status := a.itemCopy(query, &result)
	if result != 0 {
		a.cfRelease(result)
	}
	if status != errSecItemNotFound {
		t.Fatalf("SecItemCopyMatching read-only probe status=%d; want item-not-found", status)
	}
}

func TestNativeKeychainFrameworkSymbolsLoadWithoutTouchingUserKeychain(t *testing.T) {
	backend := &nativeKeychain{}
	if err := backend.load(); err != nil {
		t.Fatalf("load Keychain/CoreFoundation symbols: %v", err)
	}
	for _, constant := range []struct {
		ref  uintptr
		text string
	}{{backend.api.keyClass, "class"}, {backend.api.classGeneric, "genp"}, {backend.api.attrService, "svce"}, {backend.api.attrAccount, "acct"}, {backend.api.attrReturnData, "r_Data"}} {
		buffer := make([]byte, 64)
		if backend.api.cfStringGetCString(constant.ref, unsafe.Pointer(&buffer[0]), int64(len(buffer)), 0x08000100) == 0 {
			t.Fatalf("a required Keychain query constant was not a UTF-8 string")
		}
		end := 0
		for end < len(buffer) && buffer[end] != 0 {
			end++
		}
		if got := string(buffer[:end]); got != constant.text {
			t.Fatalf("Keychain query constant mismatch: got %q, want %q", got, constant.text)
		}
	}
	text := "Blur framework ABI check"
	ref := backend.api.string(text)
	if ref == 0 {
		t.Fatal("CFStringCreateWithBytes returned null")
	}
	backend.api.cfRelease(ref)
	value := []byte("Blur local CFData ABI check")
	data := backend.api.cfData(0, unsafe.Pointer(&value[0]), len(value))
	if data == 0 {
		t.Fatal("CFDataCreate returned null")
	}
	defer backend.api.cfRelease(data)
	if got := backend.api.cfDataLength(data); got != len(value) {
		t.Fatalf("CFDataGetLength() = %d, want %d", got, len(value))
	}
	copyValue := make([]byte, len(value))
	backend.api.cfDataGetBytes(data, cfRange{Length: len(copyValue)}, unsafe.Pointer(&copyValue[0]))
	if string(copyValue) != string(value) {
		t.Fatalf("CFDataGetBytes() = %q, want %q", copyValue, value)
	}
	dictionary, release, err := backend.api.dictionary([]uintptr{backend.api.keyClass}, []uintptr{backend.api.classGeneric})
	if err != nil || dictionary == 0 {
		t.Fatalf("CFDictionaryCreate() = %d, %v", dictionary, err)
	}
	release()
}

func TestNativeKeychainSharedQueryIncludesConfiguredAccessGroup(t *testing.T) {
	backend := &nativeKeychain{service: "sh.blur.safari", accessGroup: "TEAM123.group.sh.blur"}
	if err := backend.load(); err != nil {
		t.Fatalf("load Keychain symbols: %v", err)
	}
	a := backend.api
	query, release, err := backend.dictionary([]uintptr{a.keyClass}, []uintptr{a.classGeneric})
	if err != nil {
		t.Fatal("construct shared Keychain query")
	}
	defer release()
	if count := a.cfDictionaryGetCount(query); count != 2 {
		t.Fatalf("shared query attributes=%d, want class and access group", count)
	}
	value := a.cfDictionaryGetValue(query, a.attrAccessGroup)
	if value == 0 {
		t.Fatal("shared query omitted Keychain access group")
	}
	buffer := make([]byte, 64)
	if a.cfStringGetCString(value, unsafe.Pointer(&buffer[0]), int64(len(buffer)), 0x08000100) == 0 {
		t.Fatal("access group was not stored as a string")
	}
	if got := string(buffer[:len("TEAM123.group.sh.blur")]); got != "TEAM123.group.sh.blur" {
		t.Fatalf("access group=%q", got)
	}
}

type testKeychain struct {
	values map[string][]byte
	calls  int
}

func (k *testKeychain) get(name string, max int) ([]byte, error) {
	k.calls++
	value, ok := k.values[name]
	if !ok {
		return nil, &platform.SecretStoreError{State: platform.SecretNotFound}
	}
	if len(value) > max {
		return nil, &platform.SecretStoreError{State: platform.SecretCorrupt}
	}
	return append([]byte(nil), value...), nil
}

func (k *testKeychain) put(name string, value []byte) error {
	k.calls++
	if _, exists := k.values[name]; exists {
		return &platform.SecretStoreError{State: platform.SecretCorrupt}
	}
	k.values[name] = append([]byte(nil), value...)
	return nil
}

func (k *testKeychain) delete(name string) error {
	k.calls++
	if _, exists := k.values[name]; !exists {
		return &platform.SecretStoreError{State: platform.SecretNotFound}
	}
	delete(k.values, name)
	return nil
}

func isSecretState(err error, state platform.SecretState) bool {
	var secretErr *platform.SecretStoreError
	return errors.As(err, &secretErr) && secretErr.State == state
}

func TestKeychainStatusClassification(t *testing.T) {
	for _, tc := range []struct {
		status int32
		want   platform.SecretState
	}{
		{errSecItemNotFound, platform.SecretNotFound},
		{errSecInteractionNotAllowed, platform.SecretLocked},
		{errSecInDarkWake, platform.SecretLocked},
		{errSecNotAvailable, platform.SecretLocked},
		{errSecUserCanceled, platform.SecretUnavailable},
		{errSecAuthFailed, platform.SecretDenied},
		{errSecMissingEntitlement, platform.SecretDenied},
		{errSecDuplicateItem, platform.SecretCorrupt},
		{-50, platform.SecretUnavailable},
	} {
		var storeErr *platform.SecretStoreError
		err := keychainStatus(tc.status)
		if !errors.As(err, &storeErr) || storeErr.State != tc.want || storeErr.BackendCode != tc.status {
			t.Errorf("keychainStatus(%d) = %#v, want state %s", tc.status, err, tc.want)
		}
	}
}
