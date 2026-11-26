package client

import "net/url"

const chatGPTUsageURL = "https://chatgpt.com/settings/usage"

// validChatGPTAuthorizationURL accepts only the OpenAI authorization endpoint,
// matching the macOS account browser's check.
func validChatGPTAuthorizationURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && len(raw) <= 8192 && u.Scheme == "https" && u.Host == "auth.openai.com" && u.Path == "/api/accounts/authorize" && u.RawPath == "" && u.User == nil && u.Fragment == "" && u.Opaque == ""
}
