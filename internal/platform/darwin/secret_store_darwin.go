package darwin

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	nativeffi "github.com/goexlib/cgo"
	"github.com/hazcod/blur/internal/platform"
)

const (
	maxSecretBytes = 1 << 20
	maxSecretName  = 128
	// Keep the existing service name stable across bundle-ID changes so existing
	// encrypted stores can still resolve their data key.
	keychainService = "sh.blur.desktop"

	errSecSuccess               = 0
	errSecAuthFailed            = -25293
	errSecDuplicateItem         = -25299
	errSecItemNotFound          = -25300
	errSecInteractionNotAllowed = -25308
	errSecNotAvailable          = -25291
	errSecInDarkWake            = -25320
	errSecUserCanceled          = -128
	errSecMissingEntitlement    = -34018
)

type keychainBackend interface {
	get(name string, maxBytes int) ([]byte, error)
	put(name string, value []byte) error
	delete(name string) error
}

// SecretStore stores small keys in the current user's macOS Keychain. Values
// are never passed through a shell, process arguments, or environment.
type SecretStore struct{ backend keychainBackend }

var _ platform.SecretStore = (*SecretStore)(nil)

func NewSecretStore() *SecretStore {
	return &SecretStore{backend: &nativeKeychain{service: keychainService}}
}

// NewSharedSecretStore scopes items to an explicit service and signed Keychain
// access group. Both targets require matching entitlements; missing
// entitlements fail closed through Security.framework.
func NewSharedSecretStore(service, accessGroup string) (*SecretStore, error) {
	if !validKeychainIdentity(service, false) || !validKeychainIdentity(accessGroup, true) {
		return nil, secretError(platform.SecretUnavailable)
	}
	return &SecretStore{backend: &nativeKeychain{service: service, accessGroup: accessGroup}}, nil
}

func validKeychainIdentity(value string, group bool) bool {
	if len(value) < 3 || len(value) > 255 || value[0] == '.' || value[len(value)-1] == '.' {
		return false
	}
	dots := 0
	for i := range value {
		c := value[i]
		if c == '.' {
			dots++
			if i > 0 && value[i-1] == '.' {
				return false
			}
			continue
		}
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return !group || dots >= 2
}

func (s *SecretStore) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	report := platform.CapabilityReport{ID: "secret_store", State: platform.Available, Evidence: "keychain"}
	if err := contextError(ctx); err != nil {
		return report, err
	}
	if s == nil || s.backend == nil {
		return unavailableReport(), nil
	}
	if _, ok := s.backend.(*nativeKeychain); ok {
		if err := s.backend.(*nativeKeychain).load(); err != nil {
			return unavailableReport(), nil
		}
	}
	return report, nil
}

func (s *SecretStore) Get(ctx context.Context, name string, maxBytes int) ([]byte, error) {
	if err := validateSecretRequest(ctx, s, name, maxBytes); err != nil {
		return nil, err
	}
	value, err := s.backend.get(name, maxBytes)
	if err != nil {
		clear(value)
		return nil, safeSecretError(err)
	}
	if err := contextError(ctx); err != nil {
		clear(value)
		return nil, err
	}
	if len(value) == 0 || len(value) > maxBytes {
		clear(value)
		return nil, secretError(platform.SecretCorrupt)
	}
	copyValue := append([]byte(nil), value...)
	clear(value)
	return copyValue, nil
}

func (s *SecretStore) Put(ctx context.Context, name string, value []byte) error {
	if err := validateSecretRequest(ctx, s, name, len(value)); err != nil {
		return err
	}
	copyValue := append([]byte(nil), value...)
	defer clear(copyValue)
	if err := s.backend.put(name, copyValue); err != nil {
		return safeSecretError(err)
	}
	return contextError(ctx)
}

func (s *SecretStore) Delete(ctx context.Context, name string) error {
	if err := validateSecretRequest(ctx, s, name, 1); err != nil {
		return err
	}
	if err := s.backend.delete(name); err != nil {
		return safeSecretError(err)
	}
	return contextError(ctx)
}

func validateSecretRequest(ctx context.Context, s *SecretStore, name string, size int) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil || s.backend == nil || !validSecretName(name) || size < 1 || size > maxSecretBytes {
		return secretError(platform.SecretUnavailable)
	}
	return nil
}

func validSecretName(name string) bool {
	if len(name) == 0 || len(name) > maxSecretName {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)) {
			return false
		}
	}
	return true
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return secretError(platform.SecretUnavailable)
	}
	return ctx.Err()
}

func unavailableReport() platform.CapabilityReport {
	return platform.CapabilityReport{ID: "secret_store", State: platform.TemporarilyUnresponsive, Reason: "keychain_unavailable", Evidence: "keychain"}
}

func secretError(state platform.SecretState) error { return &platform.SecretStoreError{State: state} }

func safeSecretError(err error) error {
	var typed *platform.SecretStoreError
	if errors.As(err, &typed) && typed != nil {
		switch typed.State {
		case platform.SecretNotFound, platform.SecretLocked, platform.SecretCorrupt, platform.SecretDenied, platform.SecretUnavailable:
			return &platform.SecretStoreError{State: typed.State, BackendCode: typed.BackendCode}
		}
	}
	return secretError(platform.SecretUnavailable)
}

type nativeKeychain struct {
	once        sync.Once
	api         keychainAPI
	err         error
	service     string
	accessGroup string
}

type keychainAPI struct {
	cfHandle, secHandle                              uintptr
	cfString                                         func(uintptr, unsafe.Pointer, int, uint32, uint8) uintptr
	cfStringGetCString                               func(uintptr, unsafe.Pointer, int64, uint32) uint8
	cfData                                           func(uintptr, unsafe.Pointer, int) uintptr
	cfDictionary                                     func(uintptr, *uintptr, *uintptr, int, uintptr, uintptr) uintptr
	cfDictionaryGetValue                             func(uintptr, uintptr) uintptr
	cfDictionaryGetCount                             func(uintptr) int64
	cfRelease                                        func(uintptr)
	cfDataLength                                     func(uintptr) int
	cfDataGetBytes                                   func(uintptr, cfRange, unsafe.Pointer)
	itemCopy                                         func(uintptr, *uintptr) int32
	itemAdd                                          func(uintptr, *uintptr) int32
	itemDelete                                       func(uintptr) int32
	keyClass, classGeneric, attrService, attrAccount uintptr
	attrValueData, attrReturnData, attrAccessible    uintptr
	attrAccessGroup                                  uintptr
	accessibleWhenUnlocked                           uintptr
	trueValue, keyCallbacks, valueCallbacks          uintptr
}

func (n *nativeKeychain) load() error {
	n.once.Do(func() { n.api, n.err = loadKeychainAPI() })
	return n.err
}

func (n *nativeKeychain) get(name string, maxBytes int) ([]byte, error) {
	if err := n.load(); err != nil {
		return nil, secretError(platform.SecretUnavailable)
	}
	a := n.api
	query, owned, err := n.dictionary(
		[]uintptr{a.keyClass, a.attrService, a.attrAccount, a.attrReturnData},
		[]uintptr{a.classGeneric, a.string(n.serviceName()), a.string(name), a.trueValue},
	)
	if err != nil {
		return nil, secretError(platform.SecretUnavailable)
	}
	defer owned()
	var result uintptr
	status := a.itemCopy(query, &result)
	if status != errSecSuccess {
		return nil, keychainStatus(status)
	}
	if result == 0 {
		return nil, secretError(platform.SecretCorrupt)
	}
	defer a.cfRelease(result)
	length := a.cfDataLength(result)
	if length < 1 || length > maxBytes || length > maxSecretBytes {
		return nil, secretError(platform.SecretCorrupt)
	}
	value := make([]byte, length)
	a.cfDataGetBytes(result, cfRange{Length: length}, unsafe.Pointer(&value[0]))
	runtime.KeepAlive(value)
	return value, nil
}

type cfRange struct{ Location, Length int }

func (n *nativeKeychain) put(name string, value []byte) error {
	if err := n.load(); err != nil {
		return secretError(platform.SecretUnavailable)
	}
	a := n.api
	data := a.cfData(0, unsafe.Pointer(&value[0]), len(value))
	runtime.KeepAlive(value)
	if data == 0 {
		return secretError(platform.SecretUnavailable)
	}
	// data is released by owned() below: dictionary tracks every non-constant value.
	query, owned, err := n.dictionary(
		[]uintptr{a.keyClass, a.attrService, a.attrAccount, a.attrValueData, a.attrAccessible},
		[]uintptr{a.classGeneric, a.string(n.serviceName()), a.string(name), data, a.accessibleWhenUnlocked},
	)
	if err != nil {
		return secretError(platform.SecretUnavailable)
	}
	defer owned()
	if status := a.itemAdd(query, nil); status != errSecSuccess {
		return keychainStatus(status)
	}
	return nil
}

func (n *nativeKeychain) delete(name string) error {
	if err := n.load(); err != nil {
		return secretError(platform.SecretUnavailable)
	}
	a := n.api
	query, owned, err := n.dictionary(
		[]uintptr{a.keyClass, a.attrService, a.attrAccount},
		[]uintptr{a.classGeneric, a.string(n.serviceName()), a.string(name)},
	)
	if err != nil {
		return secretError(platform.SecretUnavailable)
	}
	defer owned()
	if status := a.itemDelete(query); status != errSecSuccess && status != errSecItemNotFound {
		return keychainStatus(status)
	}
	return nil
}

func (n *nativeKeychain) serviceName() string {
	if n != nil && n.service != "" {
		return n.service
	}
	return keychainService
}

func (n *nativeKeychain) dictionary(keys, values []uintptr) (uintptr, func(), error) {
	a := n.api
	if n.accessGroup == "" {
		return a.dictionary(keys, values)
	}
	return a.dictionary(append(keys, a.attrAccessGroup), append(values, a.string(n.accessGroup)))
}

func (a keychainAPI) string(value string) uintptr {
	ref := a.cfString(0, unsafe.Pointer(unsafe.StringData(value)), len(value), 0x08000100, 0)
	runtime.KeepAlive(value)
	return ref
}

func (a keychainAPI) dictionary(keys, values []uintptr) (uintptr, func(), error) {
	if len(keys) != len(values) || len(keys) == 0 {
		return 0, func() {}, fmt.Errorf("invalid keychain query")
	}
	owned := make([]uintptr, 0, len(keys))
	for _, value := range values {
		if value == 0 {
			for _, ref := range owned {
				a.cfRelease(ref)
			}
			return 0, func() {}, fmt.Errorf("invalid keychain query")
		}
		if value != a.classGeneric && value != a.trueValue && value != a.accessibleWhenUnlocked {
			owned = append(owned, value)
		}
	}
	for _, key := range keys {
		if key != a.keyClass && key != a.attrService && key != a.attrAccount && key != a.attrReturnData && key != a.attrValueData && key != a.attrAccessible && key != a.attrAccessGroup {
			for _, ref := range owned {
				a.cfRelease(ref)
			}
			return 0, func() {}, fmt.Errorf("invalid keychain query")
		}
	}
	dict := a.cfDictionary(0, &keys[0], &values[0], len(keys), a.keyCallbacks, a.valueCallbacks)
	if dict == 0 {
		for _, ref := range owned {
			a.cfRelease(ref)
		}
		return 0, func() {}, fmt.Errorf("keychain query allocation failed")
	}
	return dict, func() {
		a.cfRelease(dict)
		for _, ref := range owned {
			a.cfRelease(ref)
		}
	}, nil
}

func keychainStatus(status int32) error {
	state := platform.SecretUnavailable
	switch status {
	case errSecItemNotFound:
		state = platform.SecretNotFound
	case errSecInteractionNotAllowed, errSecInDarkWake, errSecNotAvailable:
		// Locked or temporarily unavailable (dark wake, keychain not ready); retry later.
		state = platform.SecretLocked
	case errSecUserCanceled:
		// The securestore contract has no cancelled state; report unavailable.
		state = platform.SecretUnavailable
	case errSecAuthFailed, errSecMissingEntitlement:
		state = platform.SecretDenied
	case errSecDuplicateItem:
		state = platform.SecretCorrupt
	}
	return &platform.SecretStoreError{State: state, BackendCode: status}
}

func loadKeychainAPI() (keychainAPI, error) {
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return keychainAPI{}, fmt.Errorf("load core foundation")
	}
	sec, err := purego.Dlopen("/System/Library/Frameworks/Security.framework/Security", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return keychainAPI{}, fmt.Errorf("load security framework")
	}
	a := keychainAPI{cfHandle: cf, secHandle: sec}
	purego.RegisterLibFunc(&a.cfString, cf, "CFStringCreateWithBytes")
	purego.RegisterLibFunc(&a.cfStringGetCString, cf, "CFStringGetCString")
	purego.RegisterLibFunc(&a.cfData, cf, "CFDataCreate")
	purego.RegisterLibFunc(&a.cfDictionary, cf, "CFDictionaryCreate")
	purego.RegisterLibFunc(&a.cfDictionaryGetValue, cf, "CFDictionaryGetValue")
	purego.RegisterLibFunc(&a.cfDictionaryGetCount, cf, "CFDictionaryGetCount")
	purego.RegisterLibFunc(&a.cfRelease, cf, "CFRelease")
	purego.RegisterLibFunc(&a.cfDataLength, cf, "CFDataGetLength")
	purego.RegisterLibFunc(&a.cfDataGetBytes, cf, "CFDataGetBytes")
	purego.RegisterLibFunc(&a.itemCopy, sec, "SecItemCopyMatching")
	purego.RegisterLibFunc(&a.itemAdd, sec, "SecItemAdd")
	purego.RegisterLibFunc(&a.itemDelete, sec, "SecItemDelete")
	for name, dest := range map[string]*uintptr{
		"kSecClass":                &a.keyClass,
		"kSecClassGenericPassword": &a.classGeneric,
		"kSecAttrService":          &a.attrService,
		"kSecAttrAccount":          &a.attrAccount,
		"kSecValueData":            &a.attrValueData,
		"kSecReturnData":           &a.attrReturnData,
		"kSecAttrAccessible":       &a.attrAccessible,
		"kSecAttrAccessGroup":      &a.attrAccessGroup,
		"kSecAttrAccessibleWhenUnlockedThisDeviceOnly": &a.accessibleWhenUnlocked,
		"kCFBooleanTrue": &a.trueValue,
	} {
		handle := cf
		if name[:4] == "kSec" {
			handle = sec
		}
		value, err := nativeffi.GetExternVariant[uintptr](handle, name)
		if err != nil || value == nil || *value == 0 {
			return keychainAPI{}, fmt.Errorf("load keychain constant")
		}
		*dest = *value
	}
	keyCallbacks, err := purego.Dlsym(cf, "kCFTypeDictionaryKeyCallBacks")
	if err != nil {
		return keychainAPI{}, fmt.Errorf("load core foundation constant")
	}
	valueCallbacks, err := purego.Dlsym(cf, "kCFTypeDictionaryValueCallBacks")
	if err != nil {
		return keychainAPI{}, fmt.Errorf("load core foundation constant")
	}
	a.keyCallbacks, a.valueCallbacks = keyCallbacks, valueCallbacks
	return a, nil
}
