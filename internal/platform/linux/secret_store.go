// Package linux contains Linux host integrations for Blur.
package linux

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"math/big"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/hazcod/blur/internal/platform"
)

const (
	secretBusName          = "org.freedesktop.secrets"
	secretRoot             = dbus.ObjectPath("/org/freedesktop/secrets")
	secretServiceIface     = "org.freedesktop.Secret.Service"
	secretItemIface        = "org.freedesktop.Secret.Item"
	secretCollection       = "org.freedesktop.Secret.Collection"
	secretSessionIface     = "org.freedesktop.Secret.Session"
	secretAppAttr          = "application"
	secretNameAttr         = "name"
	secretMaxName          = 128
	secretMaxBytes         = 1 << 20
	secretMaxMatches       = 64
	secretOperationTimeout = 5 * time.Second
	secretDHAlgorithm      = "dh-ietf1024-sha256-aes128-cbc-pkcs7"
	secretDHPrimeHex       = "FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B139B22514A08798E3404DDEF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7EDEE386BFB5A899FA5AE9F24117C4B1FE649286651ECE65381FFFFFFFFFFFFFFFF"
)

var _ platform.SecretStore = (*SecretStore)(nil)

type secretBackend interface {
	capability(context.Context) (platform.CapabilityReport, error)
	get(context.Context, string, int) ([]byte, error)
	put(context.Context, string, []byte) error
	delete(context.Context, string) error
}

// SecretStore uses the current user's Secret Service collection. Secret values
// are DH/AES encrypted in transit; item labels/lookup attributes are metadata.
// It never creates collections or launches implicit unlock prompts.
type SecretStore struct {
	backend          secretBackend
	operationTimeout time.Duration
}

func NewSecretStore() *SecretStore { return newSecretStore(secretServiceBackend{}) }

func newSecretStore(backend secretBackend) *SecretStore {
	return &SecretStore{backend: backend, operationTimeout: secretOperationTimeout}
}

func (s *SecretStore) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	report := unavailableSecretReport()
	if ctx == nil {
		return report, &platform.SecretStoreError{State: platform.SecretUnavailable}
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if s == nil || s.backend == nil {
		return report, nil
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	return s.backend.capability(opCtx)
}

func (s *SecretStore) Get(ctx context.Context, name string, maxBytes int) ([]byte, error) {
	if err := validateSecretRequest(ctx, s, name, maxBytes); err != nil {
		return nil, err
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	value, err := s.backend.get(opCtx, name, maxBytes)
	if err != nil {
		clear(value)
		if opCtx.Err() != nil {
			return nil, opCtx.Err()
		}
		return nil, safeSecretError(err)
	}
	if err := opCtx.Err(); err != nil {
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
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	copyValue := append([]byte(nil), value...)
	defer clear(copyValue)
	if err := s.backend.put(opCtx, name, copyValue); err != nil {
		if opCtx.Err() != nil {
			return opCtx.Err()
		}
		return safeSecretError(err)
	}
	return opCtx.Err()
}

func (s *SecretStore) Delete(ctx context.Context, name string) error {
	if err := validateSecretRequest(ctx, s, name, 1); err != nil {
		return err
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	if err := s.backend.delete(opCtx, name); err != nil {
		if opCtx.Err() != nil {
			return opCtx.Err()
		}
		return safeSecretError(err)
	}
	return opCtx.Err()
}

func (s *SecretStore) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := secretOperationTimeout
	if s != nil && s.operationTimeout > 0 {
		timeout = s.operationTimeout
	}
	return context.WithTimeout(parent, timeout)
}

func validateSecretRequest(ctx context.Context, store *SecretStore, name string, size int) error {
	if ctx == nil {
		return secretError(platform.SecretUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil || store.backend == nil || !validSecretName(name) || size < 1 || size > secretMaxBytes {
		return secretError(platform.SecretUnavailable)
	}
	return nil
}

func validSecretName(name string) bool {
	if len(name) == 0 || len(name) > secretMaxName {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

type secretServiceBackend struct{}

type secretWire struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

type encryptedSession struct {
	path dbus.ObjectPath
	key  []byte
}

func (b secretServiceBackend) capability(ctx context.Context) (platform.CapabilityReport, error) {
	report := unavailableSecretReport()
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return report, ctxOrSecretError(ctx, err)
	}
	defer conn.Close()
	var owns bool
	err = conn.Object("org.freedesktop.DBus", "/org/freedesktop/DBus").CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, secretBusName).Store(&owns)
	if err != nil {
		return report, ctxOrSecretError(ctx, err)
	}
	if !owns {
		return report, nil
	}
	return platform.CapabilityReport{ID: "secret_store", State: platform.Available, Reason: "secret_service_available", Evidence: "secret_service"}, nil
}

func (b secretServiceBackend) get(ctx context.Context, name string, maxBytes int) ([]byte, error) {
	conn, service, err := connectSecretService(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	item, err := findSecretItem(ctx, service, name)
	if err != nil {
		return nil, err
	}
	session, err := openEncryptedSession(ctx, service)
	if err != nil {
		return nil, err
	}
	defer session.close(service, ctx)
	var secret secretWire
	if err := conn.Object(secretBusName, item).CallWithContext(ctx, secretItemIface+".GetSecret", 0, session.path).Store(&secret); err != nil {
		clear(secret.Value)
		return nil, mapSecretDBusError(err)
	}
	if secret.Session != session.path {
		clear(secret.Value)
		return nil, secretError(platform.SecretCorrupt)
	}
	plain, err := decryptSecretTransfer(session.key, secret.Parameters, secret.Value)
	clear(secret.Value)
	if err != nil {
		return nil, secretError(platform.SecretCorrupt)
	}
	if len(plain) < 1 || len(plain) > maxBytes {
		clear(plain)
		return nil, secretError(platform.SecretCorrupt)
	}
	return plain, nil
}

func (b secretServiceBackend) put(ctx context.Context, name string, value []byte) error {
	conn, service, err := connectSecretService(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	items, locked, err := searchSecretItems(ctx, service, name)
	if err != nil {
		return err
	}
	item, state := selectSecretItem(items, locked)
	if state == platform.SecretAmbiguous || state == platform.SecretLocked {
		return secretError(state)
	}
	session, err := openEncryptedSession(ctx, service)
	if err != nil {
		return err
	}
	defer session.close(service, ctx)
	parameters, ciphertext, err := encryptSecretTransfer(session.key, value)
	if err != nil {
		return secretError(platform.SecretUnavailable)
	}
	defer clear(parameters)
	defer clear(ciphertext)
	secret := secretWire{Session: session.path, Parameters: parameters, Value: ciphertext, ContentType: "application/octet-stream"}
	if state == platform.SecretFound {
		return mapSecretDBusError(conn.Object(secretBusName, item).CallWithContext(ctx, secretItemIface+".SetSecret", 0, secret).Err)
	}
	var collection dbus.ObjectPath
	if err := service.CallWithContext(ctx, secretServiceIface+".ReadAlias", 0, "default").Store(&collection); err != nil {
		return mapSecretDBusError(err)
	}
	if !validObjectPath(collection) {
		return secretError(platform.SecretUnavailable)
	}
	attributes := secretAttributes(name)
	properties := map[string]dbus.Variant{
		secretItemIface + ".Label":      dbus.MakeVariant("Blur local data key"),
		secretItemIface + ".Attributes": dbus.MakeVariant(attributes),
	}
	var createdItem, prompt dbus.ObjectPath
	if err := conn.Object(secretBusName, collection).CallWithContext(ctx, secretCollection+".CreateItem", 0, properties, secret, false).Store(&createdItem, &prompt); err != nil {
		return mapSecretDBusError(err)
	}
	if prompt != "/" || !validObjectPath(createdItem) {
		return secretError(platform.SecretUnavailable)
	}
	return nil
}

func (b secretServiceBackend) delete(ctx context.Context, name string) error {
	conn, service, err := connectSecretService(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	items, locked, err := searchSecretItems(ctx, service, name)
	if err != nil {
		return err
	}
	item, state := selectSecretItem(items, locked)
	if state != platform.SecretFound {
		return secretError(state)
	}
	var prompt dbus.ObjectPath
	if err := conn.Object(secretBusName, item).CallWithContext(ctx, secretItemIface+".Delete", 0).Store(&prompt); err != nil {
		return mapSecretDBusError(err)
	}
	if prompt != "/" {
		return secretError(platform.SecretUnavailable)
	}
	return nil
}

func connectSecretService(ctx context.Context) (*dbus.Conn, dbus.BusObject, error) {
	if ctx == nil {
		return nil, nil, secretError(platform.SecretUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return nil, nil, ctxOrSecretError(ctx, err)
	}
	service := conn.Object(secretBusName, secretRoot)
	return conn, service, nil
}

func findSecretItem(ctx context.Context, service dbus.BusObject, name string) (dbus.ObjectPath, error) {
	items, locked, err := searchSecretItems(ctx, service, name)
	if err != nil {
		return "", err
	}
	item, state := selectSecretItem(items, locked)
	if state != platform.SecretFound {
		return "", secretError(state)
	}
	return item, nil
}

func selectSecretItem(unlocked, locked []dbus.ObjectPath) (dbus.ObjectPath, platform.SecretState) {
	if len(unlocked)+len(locked) > 1 {
		return "", platform.SecretAmbiguous
	}
	if len(locked) == 1 {
		return "", platform.SecretLocked
	}
	if len(unlocked) == 1 {
		return unlocked[0], platform.SecretFound
	}
	return "", platform.SecretNotFound
}

func searchSecretItems(ctx context.Context, service dbus.BusObject, name string) ([]dbus.ObjectPath, []dbus.ObjectPath, error) {
	var unlocked, locked []dbus.ObjectPath
	err := service.CallWithContext(ctx, secretServiceIface+".SearchItems", 0, secretAttributes(name)).Store(&unlocked, &locked)
	if err != nil {
		return nil, nil, mapSecretDBusError(err)
	}
	if len(unlocked) > secretMaxMatches || len(locked) > secretMaxMatches || len(unlocked)+len(locked) > secretMaxMatches {
		return nil, nil, secretError(platform.SecretAmbiguous)
	}
	for _, path := range append(append([]dbus.ObjectPath(nil), unlocked...), locked...) {
		if !validObjectPath(path) {
			return nil, nil, secretError(platform.SecretCorrupt)
		}
	}
	return unlocked, locked, nil
}

func secretAttributes(name string) map[string]string {
	return map[string]string{secretAppAttr: "sh.blur.desktop", secretNameAttr: name}
}

func openEncryptedSession(ctx context.Context, service dbus.BusObject) (*encryptedSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prime, ok := new(big.Int).SetString(secretDHPrimeHex, 16)
	if !ok || prime.BitLen() != 1024 {
		return nil, secretError(platform.SecretUnavailable)
	}
	max := new(big.Int).Sub(prime, big.NewInt(3))
	private, err := rand.Int(rand.Reader, max)
	if err != nil {
		return nil, secretError(platform.SecretUnavailable)
	}
	private.Add(private, big.NewInt(2))
	clientPublic := new(big.Int).Exp(big.NewInt(2), private, prime)
	clientBytes := clientPublic.Bytes()
	var output dbus.Variant
	var path dbus.ObjectPath
	err = service.CallWithContext(ctx, secretServiceIface+".OpenSession", 0, secretDHAlgorithm, dbus.MakeVariant(clientBytes)).Store(&output, &path)
	clear(clientBytes)
	if err != nil {
		return nil, mapSecretDBusError(err)
	}
	if !validObjectPath(path) {
		return nil, secretError(platform.SecretUnavailable)
	}
	serverBytes, ok := output.Value().([]byte)
	if !ok || len(serverBytes) == 0 || len(serverBytes) > 128 {
		return nil, secretError(platform.SecretCorrupt)
	}
	serverPublic := new(big.Int).SetBytes(serverBytes)
	clear(serverBytes)
	if serverPublic.Cmp(big.NewInt(1)) <= 0 || serverPublic.Cmp(new(big.Int).Sub(prime, big.NewInt(1))) >= 0 {
		return nil, secretError(platform.SecretCorrupt)
	}
	key, err := deriveSecretSessionKey(private, serverPublic, prime)
	private.SetInt64(0)
	serverPublic.SetInt64(0)
	if err != nil {
		return nil, secretError(platform.SecretUnavailable)
	}
	if err := ctx.Err(); err != nil {
		clear(key)
		return nil, err
	}
	return &encryptedSession{path: path, key: key}, nil
}

func deriveSecretSessionKey(private, peerPublic, prime *big.Int) ([]byte, error) {
	if private == nil || peerPublic == nil || prime == nil || prime.BitLen() != 1024 || private.Cmp(big.NewInt(1)) <= 0 || private.Cmp(new(big.Int).Sub(prime, big.NewInt(1))) >= 0 || peerPublic.Cmp(big.NewInt(1)) <= 0 || peerPublic.Cmp(new(big.Int).Sub(prime, big.NewInt(1))) >= 0 {
		return nil, errors.New("invalid DH value")
	}
	shared := new(big.Int).Exp(peerPublic, private, prime)
	sharedBytes := shared.FillBytes(make([]byte, 128))
	shared.SetInt64(0)
	defer clear(sharedBytes)
	return hkdf.Key(sha256.New, sharedBytes, nil, "", aes.BlockSize)
}

func (s *encryptedSession) close(service dbus.BusObject, ctx context.Context) {
	if s == nil {
		return
	}
	clear(s.key)
	if s.path != "" && s.path != "/" && ctx != nil && ctx.Err() == nil {
		_ = service.CallWithContext(ctx, secretSessionIface+".Close", 0).Err
	}
}

func encryptSecretTransfer(key, plain []byte) (iv, ciphertext []byte, err error) {
	if len(key) != 16 || len(plain) == 0 || len(plain) > secretMaxBytes {
		return nil, nil, errors.New("invalid transfer value")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	iv = make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		clear(iv)
		return nil, nil, err
	}
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	padded := make([]byte, len(plain)+padding)
	copy(padded, plain)
	for i := len(plain); i < len(padded); i++ {
		padded[i] = byte(padding)
	}
	ciphertext = make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)
	clear(padded)
	return iv, ciphertext, nil
}

func decryptSecretTransfer(key, iv, ciphertext []byte) ([]byte, error) {
	if len(key) != 16 || len(iv) != aes.BlockSize || len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 || len(ciphertext) > secretMaxBytes+aes.BlockSize {
		return nil, errors.New("invalid encrypted transfer")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ciphertext)
	padding := int(plain[len(plain)-1])
	if padding < 1 || padding > aes.BlockSize || padding > len(plain) {
		clear(plain)
		return nil, errors.New("invalid encrypted transfer padding")
	}
	var mismatch byte
	for _, b := range plain[len(plain)-padding:] {
		mismatch |= b ^ byte(padding)
	}
	if mismatch != 0 {
		clear(plain)
		return nil, errors.New("invalid encrypted transfer padding")
	}
	result := append([]byte(nil), plain[:len(plain)-padding]...)
	clear(plain)
	return result, nil
}

func validObjectPath(path dbus.ObjectPath) bool { return path != "/" && path.IsValid() }

func mapSecretDBusError(err error) error {
	if err == nil {
		return nil
	}
	var busErr dbus.Error
	if errors.As(err, &busErr) {
		switch busErr.Name {
		case "org.freedesktop.Secret.Error.IsLocked":
			return secretError(platform.SecretLocked)
		case "org.freedesktop.Secret.Error.NoSuchObject", "org.freedesktop.DBus.Error.UnknownObject":
			return secretError(platform.SecretNotFound)
		case "org.freedesktop.DBus.Error.AccessDenied", "org.freedesktop.Secret.Error.AccessDenied":
			return secretError(platform.SecretDenied)
		}
	}
	return secretError(platform.SecretUnavailable)
}

func ctxOrSecretError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	return secretError(platform.SecretUnavailable)
}

func safeSecretError(err error) error {
	if err == nil {
		return nil
	}
	var secretErr *platform.SecretStoreError
	if errors.As(err, &secretErr) && secretErr != nil {
		switch secretErr.State {
		case platform.SecretNotFound, platform.SecretLocked, platform.SecretCorrupt, platform.SecretDenied, platform.SecretUnavailable, platform.SecretAmbiguous:
			return &platform.SecretStoreError{State: secretErr.State}
		}
	}
	return secretError(platform.SecretUnavailable)
}

func secretError(state platform.SecretState) error { return &platform.SecretStoreError{State: state} }

func unavailableSecretReport() platform.CapabilityReport {
	return platform.CapabilityReport{ID: "secret_store", State: platform.TemporarilyUnresponsive, Reason: "service_unavailable", Evidence: "secret_service"}
}
