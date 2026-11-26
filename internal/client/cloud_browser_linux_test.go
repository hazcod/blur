//go:build linux

package client

import "testing"

func TestValidChatGPTAuthorizationURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://auth.openai.com/api/accounts/authorize?client_id=x": true,
		"http://auth.openai.com/api/accounts/authorize":              false,
		"https://evil.example/api/accounts/authorize":                false,
		"https://auth.openai.com/other":                              false,
		"https://user@auth.openai.com/api/accounts/authorize":        false,
		"https://auth.openai.com/api/accounts/authorize#fragment":    false,
	} {
		if got := validChatGPTAuthorizationURL(raw); got != want {
			t.Errorf("validChatGPTAuthorizationURL(%q) = %v, want %v", raw, got, want)
		}
	}
}
