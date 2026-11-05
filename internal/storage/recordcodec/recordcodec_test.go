package recordcodec

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func testKey(seed byte) []byte {
	return bytes.Repeat([]byte{seed}, 32)
}

func testMetadata() RecordMetadata {
	return RecordMetadata{Namespace: "observations", Key: "content-001", Revision: 3}
}

func TestDeriveKeyMatchesRFC5869SHA256Vector(t *testing.T) {
	secret := bytes.Repeat([]byte{0x0b}, 22)
	salt, err := hex.DecodeString("000102030405060708090a0b0c")
	if err != nil {
		t.Fatal(err)
	}
	got, err := deriveKey(secret, salt, "\xf0\xf1\xf2\xf3\xf4\xf5\xf6\xf7\xf8\xf9", 42)
	if err != nil {
		t.Fatalf("deriveKey() error = %v", err)
	}
	const wantHex = "3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865"
	if gotHex := hex.EncodeToString(got); gotHex != wantHex {
		t.Fatalf("deriveKey() = %s, want RFC 5869 output %s", gotHex, wantHex)
	}
}

func TestSealOpenRoundTripAcrossBoundedPayloads(t *testing.T) {
	codec, err := NewCodec(testKey(0x6b))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = codec.Close() })

	for name, plaintext := range map[string][]byte{
		"empty":   {},
		"text":    []byte("synthetic café memo with no user data"),
		"binary":  {0, 1, 0xff, 0x80, 7},
		"maximum": bytes.Repeat([]byte("x"), MaxPlaintextBytes),
	} {
		t.Run(name, func(t *testing.T) {
			envelope, err := codec.Seal(testMetadata(), plaintext)
			if err != nil {
				t.Fatalf("Seal() error = %v", err)
			}
			got, err := codec.Open(testMetadata(), envelope)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			if !bytes.Equal(got, plaintext) {
				t.Fatalf("Open() = %q, want original bounded payload", got)
			}
		})
	}
}

func TestSealUsesFreshUniqueNonces(t *testing.T) {
	codec, err := NewCodec(testKey(0x31))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = codec.Close() })

	seen := make(map[string]struct{}, 512)
	for i := 0; i < 512; i++ {
		envelope, err := codec.Seal(testMetadata(), []byte("same synthetic record"))
		if err != nil {
			t.Fatalf("Seal() call %d: %v", i, err)
		}
		if len(envelope) < nonceOffset+NonceSize {
			t.Fatalf("Seal() envelope length = %d, too short for a nonce", len(envelope))
		}
		nonce := string(envelope[nonceOffset : nonceOffset+NonceSize])
		if _, duplicate := seen[nonce]; duplicate {
			t.Fatalf("Seal() reused a nonce on call %d", i)
		}
		seen[nonce] = struct{}{}
	}
}

func TestOpenRejectsWrongKey(t *testing.T) {
	writer, err := NewCodec(testKey(0x11))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := NewCodec(testKey(0x12))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	envelope, err := writer.Seal(testMetadata(), []byte("synthetic protected payload"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := reader.Open(testMetadata(), envelope)
	if !errors.Is(err, ErrAuthentication) || got != nil {
		t.Fatalf("Open() = (%q, %v), want authentication failure without plaintext", got, err)
	}
}

func TestOpenAuthenticatesNamespaceKeyAndRevision(t *testing.T) {
	codec, err := NewCodec(testKey(0x51))
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()

	metadata := testMetadata()
	envelope, err := codec.Seal(metadata, []byte("authenticated synthetic record"))
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]RecordMetadata{
		"namespace": {Namespace: "derivations", Key: metadata.Key, Revision: metadata.Revision},
		"key":       {Namespace: metadata.Namespace, Key: "content-002", Revision: metadata.Revision},
		"revision":  {Namespace: metadata.Namespace, Key: metadata.Key, Revision: metadata.Revision + 1},
	}
	for name, swapped := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := codec.Open(swapped, envelope)
			if !errors.Is(err, ErrAuthentication) || got != nil {
				t.Fatalf("Open() = (%q, %v), want authentication failure without plaintext", got, err)
			}
		})
	}
}

func TestOpenRejectsEverySingleByteBitFlip(t *testing.T) {
	codec, err := NewCodec(testKey(0x61))
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()

	original, err := codec.Seal(testMetadata(), []byte("tamper detection fixture"))
	if err != nil {
		t.Fatal(err)
	}
	for offset := range original {
		for bit := byte(1); bit != 0; bit <<= 1 {
			tampered := bytes.Clone(original)
			tampered[offset] ^= bit
			got, err := codec.Open(testMetadata(), tampered)
			if err == nil || got != nil {
				t.Fatalf("Open() released plaintext after bit flip at byte %d: %q", offset, got)
			}
		}
	}
}

func TestOpenRejectsUnsupportedEnvelopeVersion(t *testing.T) {
	codec, err := NewCodec(testKey(0x60))
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()

	envelope, err := codec.Seal(testMetadata(), []byte("version fixture"))
	if err != nil {
		t.Fatal(err)
	}
	envelope[0] = envelopeVersion + 1
	if got, err := codec.Open(testMetadata(), envelope); !errors.Is(err, ErrUnsupportedVersion) || got != nil {
		t.Fatalf("Open(unsupported version) = (%q, %v), want ErrUnsupportedVersion", got, err)
	}
}

func TestOpenRejectsEveryTruncatedPrefix(t *testing.T) {
	codec, err := NewCodec(testKey(0x62))
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()

	envelope, err := codec.Seal(testMetadata(), []byte("truncation fixture"))
	if err != nil {
		t.Fatal(err)
	}
	for end := 0; end < len(envelope); end++ {
		got, err := codec.Open(testMetadata(), envelope[:end])
		if err == nil || got != nil {
			t.Fatalf("Open() accepted prefix of %d bytes: %q", end, got)
		}
	}
}

func TestCodecRejectsOversizeInputs(t *testing.T) {
	codec, err := NewCodec(testKey(0x63))
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()

	if _, err := codec.Seal(testMetadata(), make([]byte, MaxPlaintextBytes+1)); !errors.Is(err, ErrOversize) {
		t.Fatalf("Seal(oversize) error = %v, want ErrOversize", err)
	}
	if _, err := codec.Open(testMetadata(), make([]byte, MaxEnvelopeBytes+1)); !errors.Is(err, ErrOversize) {
		t.Fatalf("Open(oversize) error = %v, want ErrOversize", err)
	}
}

func TestCodecRejectsInvalidUTF8Metadata(t *testing.T) {
	codec, err := NewCodec(testKey(0x64))
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()

	metadata := testMetadata()
	metadata.Namespace = string([]byte{'o', 'b', 0xff, 's'})
	if _, err := codec.Seal(metadata, []byte("synthetic")); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("Seal(invalid metadata) error = %v, want ErrInvalidMetadata", err)
	}
}

func TestCodecRejectsInvalidKeysAndMetadataBounds(t *testing.T) {
	if _, err := NewCodec([]byte("short")); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("NewCodec(short key) error = %v, want ErrInvalidKey", err)
	}
	codec, err := NewCodec(testKey(0x65))
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()

	for name, metadata := range map[string]RecordMetadata{
		"empty namespace": {Namespace: "", Key: "k", Revision: 1},
		"empty key":       {Namespace: "records", Key: "", Revision: 1},
		"zero revision":   {Namespace: "records", Key: "k", Revision: 0},
		"long namespace":  {Namespace: string(bytes.Repeat([]byte("n"), MaxNamespaceBytes+1)), Key: "k", Revision: 1},
		"long key":        {Namespace: "records", Key: string(bytes.Repeat([]byte("k"), MaxRecordKeyBytes+1)), Revision: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := codec.Seal(metadata, []byte("synthetic")); !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("Seal(invalid metadata) error = %v, want ErrInvalidMetadata", err)
			}
		})
	}
}

func TestCodecCopiesKeyInputAndClearsOnClose(t *testing.T) {
	root := testKey(0x66)
	codec, err := NewCodec(root)
	if err != nil {
		t.Fatal(err)
	}
	clear(root)
	peer, err := NewCodec(testKey(0x66))
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	envelope, err := codec.Seal(testMetadata(), []byte("key-copy fixture"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := peer.Open(testMetadata(), envelope); err != nil || string(got) != "key-copy fixture" {
		t.Fatalf("peer.Open() = (%q, %v), want copied input key", got, err)
	}
	if err := codec.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if len(codec.state.key) != 0 {
		t.Fatal("Close() retained the derived key buffer")
	}
	if got, err := codec.Open(testMetadata(), envelope); !errors.Is(err, ErrClosed) || got != nil {
		t.Fatalf("Open() after Close() = (%q, %v), want ErrClosed without plaintext", got, err)
	}
}

func TestCodecFormattingAndJSONDoNotExposeKey(t *testing.T) {
	key := bytes.Repeat([]byte("K"), 32)
	codec, err := NewCodec(key)
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()

	secretHex := hex.EncodeToString(key)
	for _, formatted := range []string{
		fmt.Sprintf("%v", codec), fmt.Sprintf("%+v", codec), fmt.Sprintf("%#v", codec),
		fmt.Sprintf("%s", codec), fmt.Sprintf("%q", codec),
	} {
		if bytes.Contains([]byte(formatted), key) || bytes.Contains([]byte(formatted), []byte(secretHex)) {
			t.Fatalf("Codec formatting exposed key material: %q", formatted)
		}
	}
	encoded, err := json.Marshal(codec)
	if !errors.Is(err, ErrSerialization) || len(encoded) != 0 {
		t.Fatalf("json.Marshal(codec) = (%s, %v), want serialization refusal without bytes", encoded, err)
	}
}

func TestCodecFormattingRedactsValuesCopiesAndCommonVerbs(t *testing.T) {
	codec, err := NewCodec(testKey(0x5a))
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()

	value := *codec
	copyValue := value
	values := []any{codec, *codec, value, &value, copyValue, &copyValue}
	verbs := []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%p", "%b", "%d", "%o", "%U", "%T"}
	derivedKey := hex.EncodeToString(codec.state.key)
	for _, candidate := range values {
		for _, verb := range verbs {
			formatted := fmt.Sprintf(verb, candidate)
			if (verb != "%p" && verb != "%T" && formatted != "recordcodec.Codec([redacted])") || strings.Contains(formatted, derivedKey) {
				t.Errorf("fmt.Sprintf(%q, %T) = %q, want redacted marker without key material", verb, candidate, formatted)
			}
		}
	}
}

func FuzzOpenRejectsUnauthenticatedEnvelope(f *testing.F) {
	codec, err := NewCodec(testKey(0x71))
	if err != nil {
		f.Fatal(err)
	}
	metadata := testMetadata()
	valid, err := codec.Seal(metadata, []byte("fuzz fixture"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte{})
	f.Add([]byte{1, 2, 3})
	f.Fuzz(func(t *testing.T, envelope []byte) {
		got, err := codec.Open(metadata, envelope)
		if err == nil && !bytes.Equal(got, []byte("fuzz fixture")) {
			t.Fatalf("Open() accepted malformed envelope as %q", got)
		}
	})
	if err := codec.Close(); err != nil {
		f.Fatal(err)
	}
}
