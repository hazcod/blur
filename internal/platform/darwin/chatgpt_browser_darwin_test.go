package darwin

import "testing"

func TestChatGPTAuthorizationBrowserRestrictsOfficialOriginAndRoute(t *testing.T) {
	for _, raw := range []string{"https://evil.test/api/accounts/authorize", "http://auth.openai.com/api/accounts/authorize", "https://auth.openai.com:8443/api/accounts/authorize", "https://user@auth.openai.com/api/accounts/authorize", "https://auth.openai.com/backend-api/authorize", "https://auth.openai.com/api/accounts/authorize#fragment"} {
		if validChatGPTAuthorizationURL(raw) {
			t.Fatalf("untrusted browser destination accepted: %q", raw)
		}
		if err := OpenChatGPTAuthorization(raw); err == nil {
			t.Fatal("untrusted browser destination opened")
		}
	}
	if !validChatGPTAuthorizationURL("https://auth.openai.com/api/accounts/authorize?state=synthetic") {
		t.Fatal("official authorization route was rejected")
	}
}
