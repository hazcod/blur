package privacy

import (
	"strings"
	"testing"
)

func TestRedactRecognizableSecretsAndPreserveProse(t *testing.T) {
	in := `A normal meeting note. password: hunter2
Authorization: Bearer fixturetoken
https://alice:pw@example.test/path?token=abc&view=day
sk-12345678901234567890123456789012 ghp_123456789012345678901234567890123456 AWS AKIAIOSFODNN7EXAMPLE xoxb-1234567890-1234567890-abcdefghijklmnop
SSN 078-05-1120 card 4111 1111 1111 1111 CVV: 123 IBAN GB82 WEST 1234 5698 7654 32
-----BEGIN PRIVATE KEY-----
fixture
-----END PRIVATE KEY-----`
	out, counts := Redact(in)
	if !strings.Contains(out, "A normal meeting note.") || !strings.Contains(out, "view=day") {
		t.Fatalf("ordinary text lost: %q", out)
	}
	for _, secret := range []string{"hunter2", "fixturetoken", "alice:pw", "token=abc", "sk-123456", "ghp_123", "AKIAIOS", "xoxb-123", "078-05-1120", "4111 1111", "CVV: 123", "GB82 WEST", "fixture"} {
		if strings.Contains(out, secret) {
			t.Errorf("secret survived: %s", secret)
		}
	}
	if len(counts) < 8 {
		t.Fatalf("too few aggregate redactions: %+v", counts)
	}
}

func TestRedactIsIdempotentAndPlaceholdersFixed(t *testing.T) {
	a, _ := Redact("password: short\npassword: muchlongerfixture")
	if strings.Count(a, "[REDACTED:SECRET]") != 2 {
		t.Fatalf("nonfixed placeholders: %q", a)
	}
	b, _ := Redact(a)
	if b != a {
		t.Fatalf("second pass changed text: %q", b)
	}
}

func TestRedactValidatesCardAndIBAN(t *testing.T) {
	in := "reference 4111 1111 1111 1112 and GB82 WEST 1234 5698 7654 33"
	out, counts := Redact(in)
	if out != in || len(counts) != 0 {
		t.Fatalf("invalid numbers masked: %q %+v", out, counts)
	}
}

func TestURLCredentialsHaveFixedIdempotentPlaceholder(t *testing.T) {
	a, counts := Redact("https://alice:short@example.test/path?token=abc&view=day")
	if !strings.Contains(a, "[REDACTED:URL_CREDENTIAL]") || strings.Contains(a, "alice") || !strings.Contains(a, "view=day") || len(counts) != 2 {
		t.Fatalf("URL redaction: %q %+v", a, counts)
	}
	b, later := Redact(a)
	if a != b || len(later) != 0 {
		t.Fatalf("URL reprocessing: %q %q %+v", a, b, later)
	}
}

func TestOpenAIProjectTokenAndClientSecretAreMasked(t *testing.T) {
	in := "sk-proj-12345678901234567890123456789012 https://example.test/?client_secret=synthetic&view=day"
	out, _ := Redact(in)
	if strings.Contains(out, "sk-proj-") || strings.Contains(out, "synthetic") || !strings.Contains(out, "view=day") {
		t.Fatalf("sensitive values survived: %q", out)
	}
}
