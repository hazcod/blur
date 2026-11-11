package capture

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/privacy"
)

func TestAuthenticatedObservationRecordRoundTripsDeterministically(t *testing.T) {
	original, err := SanitizeObservation("example.app", "example.com", "password: x\n"+strings.Repeat("x", 52), 64)
	if err != nil {
		t.Fatal(err)
	}
	if !original.Truncated() || len(original.RedactionCounts()) == 0 {
		t.Fatal("fixture must include both truncation and redaction metadata")
	}

	first, err := MarshalAuthenticatedObservation(original)
	if err != nil {
		t.Fatalf("MarshalAuthenticatedObservation() error = %v", err)
	}
	second, err := MarshalAuthenticatedObservation(original)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("record encoding is not deterministic: %v", err)
	}
	restored, err := RestoreAuthenticatedObservation(first)
	if err != nil || !reflect.DeepEqual(restored, original) {
		t.Fatalf("RestoreAuthenticatedObservation() differs: err=%v", err)
	}
}

func TestRestoreAuthenticatedObservationRejectsMalformedRecords(t *testing.T) {
	original, err := SanitizeObservation("example.app", "example.com", "password: hunter2", 128)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := MarshalAuthenticatedObservation(original)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data []byte
	}{
		{name: "bit flip", data: func() []byte { v := bytes.Clone(valid); v[len(v)-1] ^= 1; return v }()},
		{name: "unsupported format version", data: func() []byte { v := bytes.Clone(valid); v[0]++; return v }()},
		{name: "unknown flags are noncanonical", data: func() []byte { v := bytes.Clone(valid); v[1] |= 0x80; return v }()},
		{name: "trailing bytes", data: append(bytes.Clone(valid), 0)},
		{name: "wrong redaction version", data: func() []byte {
			v := bytes.Clone(valid)
			binary.BigEndian.PutUint16(v[2:4], uint16(privacy.Version+1))
			return v
		}()},
		{name: "oversize", data: make([]byte, MaxObservationTextBytes+8193)},
	}
	for end := 0; end < len(valid); end++ {
		tests = append(tests, struct {
			name string
			data []byte
		}{name: "truncated prefix", data: bytes.Clone(valid[:end])})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := RestoreAuthenticatedObservation(test.data)
			if err == nil || got.Validate() == nil {
				t.Fatalf("RestoreAuthenticatedObservation() accepted malformed record as %+v", got)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("restore error disclosed plaintext: %v", err)
			}
		})
	}
}

func TestRestoreAuthenticatedObservationRejectsNoncanonicalPrivateFields(t *testing.T) {
	original, err := SanitizeObservation("example.app", "example.com", "safe text", 128)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := MarshalAuthenticatedObservation(original)
	if err != nil {
		t.Fatal(err)
	}
	appLength := int(binary.BigEndian.Uint32(encoded[4:8]))
	domainLength := int(binary.BigEndian.Uint32(encoded[8:12]))
	appOffset := 49
	if appLength != len(original.AppID()) || domainLength != len(original.Domain()) {
		t.Fatalf("unexpected record header lengths: app=%d domain=%d", appLength, domainLength)
	}
	noncanonical := bytes.Clone(encoded)
	copy(noncanonical[appOffset:appOffset+appLength], "Example.app")
	mutated := original.Clone()
	mutated.appID = "Example.app"
	mutated.contentID = contentID(mutated)
	id, err := hex.DecodeString(mutated.contentID)
	if err != nil {
		t.Fatal(err)
	}
	copy(noncanonical[17:49], id)
	if got, err := RestoreAuthenticatedObservation(noncanonical); err == nil || got.Validate() == nil {
		t.Fatal("RestoreAuthenticatedObservation() accepted noncanonical app identity")
	}
}

func FuzzRestoreAuthenticatedObservation(f *testing.F) {
	original, err := SanitizeObservation("example.app", "example.com", "password: seed", 128)
	if err != nil {
		f.Fatal(err)
	}
	valid, err := MarshalAuthenticatedObservation(original)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte{})
	f.Add([]byte{1, 2, 3})
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := RestoreAuthenticatedObservation(data)
		if err == nil && got.Validate() != nil {
			t.Fatal("restore returned an invalid observation")
		}
	})
}
