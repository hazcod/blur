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

func TestPlaceholderShapedTextDoesNotBypassWholeMatch(t *testing.T) {
	in := "Authorization: [REDACTED:SECRET] Bearer actual-secret\n" +
		"-----BEGIN PRIVATE KEY-----\n[REDACTED:SECRET] actual-key-material\n-----END PRIVATE KEY-----"
	out, _ := Redact(in)
	if strings.Contains(out, "actual-secret") || strings.Contains(out, "actual-key-material") || !strings.Contains(out, "[REDACTED:AUTHORIZATION]") || !strings.Contains(out, "[REDACTED:PRIVATE_KEY]") {
		t.Fatalf("whole-match bypass: %q", out)
	}
	a, _ := Redact(out)
	if a != out {
		t.Fatalf("non-idempotent whole-match masking: %q", a)
	}
}

func TestRepeatedSensitiveQueryValuesAllCollapse(t *testing.T) {
	in := "https://example.test/?client_secret=%5BREDACTED%3AURL_QUERY%5D&client_secret=actual-secret&view=day"
	out, counts := Redact(in)
	if strings.Contains(out, "actual-secret") || strings.Count(out, "client_secret=") != 1 || !strings.Contains(out, "view=day") || len(counts) != 1 || counts[0].Count != 1 {
		t.Fatalf("repeated query bypass: %q %+v", out, counts)
	}
	again, later := Redact(out)
	if again != out || len(later) != 0 {
		t.Fatalf("query idempotence: %q %q %+v", out, again, later)
	}
}

func TestLabelledSecretContainingRedactedSubstringIsMasked(t *testing.T) {
	in := "password: MY_REDACTED_PASSWORD_123\napi_key=prefix[REDACTED:SECRET]suffix"
	out, counts := Redact(in)
	if strings.Contains(out, "MY_REDACTED_PASSWORD_123") || strings.Contains(out, "prefix") || len(counts) != 1 || counts[0].Count != 2 {
		t.Fatalf("label bypass: %q %+v", out, counts)
	}
	again, later := Redact(out)
	if again != out || len(later) != 0 {
		t.Fatalf("label idempotence: %q %q %+v", out, again, later)
	}
}

func TestJSONQuotedSecretsAreMasked(t *testing.T) {
	in := `{"password": "actual-secret", "api_key" :"key-value", "token":"[REDACTED:SECRET]"}`
	out, counts := Redact(in)
	if strings.Contains(out, "actual-secret") || strings.Contains(out, "key-value") || !strings.Contains(out, `"password": "[REDACTED:SECRET]"`) || !strings.Contains(out, `"api_key" :"[REDACTED:SECRET]"`) || len(counts) != 1 || counts[0].Count != 2 {
		t.Fatalf("JSON secret redaction: %q %+v", out, counts)
	}
	again, later := Redact(out)
	if again != out || len(later) != 0 {
		t.Fatalf("JSON idempotence: %q %q %+v", out, again, later)
	}
}

func TestURLCredentialPlaceholderWithPasswordIsMasked(t *testing.T) {
	out, counts := Redact("https://[REDACTED:URL_CREDENTIAL]:actual-secret@example.test/")
	if strings.Contains(out, "actual-secret") || !strings.Contains(out, "[REDACTED:URL_CREDENTIAL]") || len(counts) != 1 {
		t.Fatalf("userinfo bypass: %q %+v", out, counts)
	}
}

func TestPlainQuotedLabelMasksEntireValue(t *testing.T) {
	out, _ := Redact(`password: "two word secret"`)
	if strings.Contains(out, "word secret") || !strings.Contains(out, `"[REDACTED:SECRET]"`) {
		t.Fatalf("quoted label: %q", out)
	}
}

func TestLabelPlaceholderFollowedBySecretIsNotAccepted(t *testing.T) {
	out, _ := Redact("password: [REDACTED:SECRET] actual-secret")
	if strings.Contains(out, "actual-secret") || out != "password: [REDACTED:SECRET]" {
		t.Fatalf("label suffix bypass: %q", out)
	}
}

func TestMalformedURLSensitiveComponentsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		secrets  []string
	}{
		{"empty host", "http:///path?token=actual-secret", []string{"actual-secret"}},
		{"invalid host escape", "http://exa%ZZmple.test/path?client_secret=actual-secret", []string{"actual-secret"}},
		{"invalid query escape", "https://example.test/path?token=actual-secret%ZZ", []string{"actual-secret"}},
		{"repeated keys", "http:///path?client_secret=first-secret&client_secret=second-secret", []string{"first-secret", "second-secret"}},
		{"userinfo", "http://alice:password@/path", []string{"alice:password"}},
		{"raw userinfo with invalid escape", "http://alice%ZZ:pw@/path", []string{"alice%ZZ:pw"}},
		{"encoded userinfo with invalid escape", "http://alice%ZZ%3Apw%40/path", []string{"alice%ZZ%3Apw%40"}},
		{"double encoded userinfo with invalid escape", "http://alice%ZZ%2540/path", []string{"alice%ZZ%2540"}},
		{"split encoded userinfo with invalid escape", "http://alice%ZZ%25%34%30/path", []string{"alice%ZZ%25%34%30"}},
		{"encoded key and value", "http:///path?%74oken=actual%2Dsecret", []string{"actual%2Dsecret"}},
		{"double encoded key", "http:///path?%2574oken=actual-secret", []string{"actual-secret"}},
		{"encoded userinfo", "http://alice%3Apw%40/path", []string{"alice%3Apw%40"}},
		{"encoded equals", "http:///path?token%3Dactual-secret", []string{"actual-secret"}},
		{"placeholder suffix", "http:///path?token=[REDACTED:URL_QUERY]actual-secret", []string{"actual-secret"}},
		{"userinfo placeholder suffix", "http://[REDACTED:URL_CREDENTIAL]:actual-secret@/path", []string{"actual-secret"}},
		{"userinfo placeholder appended data", "http://[REDACTED:URL_CREDENTIAL]actual-secret@/path", []string{"actual-secret"}},
		{"userinfo placeholder invalid escape", "http://[REDACTED:URL_CREDENTIAL]%ZZ:actual-secret@/path", []string{"actual-secret"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, counts := Redact(tc.in)
			for _, secret := range tc.secrets {
				if strings.Contains(out, secret) {
					t.Fatalf("secret survived in %q: %q", tc.in, out)
				}
			}
			if len(counts) == 0 {
				t.Fatalf("missing aggregate count: %q", out)
			}
			again, later := Redact(out)
			if again != out || len(later) != 0 {
				t.Fatalf("non-idempotent masking: %q %q %+v", out, again, later)
			}
		})
	}
}

func TestMalformedURLBenignTextAndExactPlaceholderRemainStable(t *testing.T) {
	for _, in := range []string{
		"http:///path?view=day",
		"http://exa%ZZmple.test/path?view=day",
		"https://example.test:8080/path?view=%ZZ",
		"http://example.test:abc/path?view=day",
		"http://[REDACTED:URL_CREDENTIAL]@/path",
		"http:///path?token=[REDACTED:URL_QUERY]",
		"https://example.test/?token=%5BREDACTED%3AURL_QUERY%5D",
	} {
		out, counts := Redact(in)
		if out != in || len(counts) != 0 {
			t.Fatalf("changed benign or exact value: %q -> %q %+v", in, out, counts)
		}
	}
	appended := "https://example.test/?token=%5BREDACTED%3AURL_QUERY%5Dactual-secret"
	out, _ := Redact(appended)
	if strings.Contains(out, "actual-secret") {
		t.Fatalf("appended value survived: %q", out)
	}
	doubleKey := "https://example.test/?%2574oken=actual-secret"
	out, _ = Redact(doubleKey)
	if strings.Contains(out, "actual-secret") {
		t.Fatalf("double encoded valid key survived: %q", out)
	}
}

func TestMalformedURLDoesNotHideAdjacentLabelledSecret(t *testing.T) {
	in := "ordinary http:///path?token=first-secret password: second-secret"
	out, counts := Redact(in)
	if strings.Contains(out, "first-secret") || strings.Contains(out, "second-secret") || !strings.Contains(out, "ordinary") || len(counts) < 2 {
		t.Fatalf("adjacent secret: %q %+v", out, counts)
	}
}
