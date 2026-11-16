// Package modeltest provides explicit local-model metadata for synthetic HTTP
// fixtures. Production code must use the real provider metadata preflight.
package modeltest

import (
	"encoding/json"
	"net/http"
	"strings"
)

func LocalOllama(name string, next http.Handler) http.Handler {
	if !strings.Contains(strings.TrimPrefix(name, "localhost/"), ":") {
		name += ":latest"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": name, "digest": strings.Repeat("a", 64)}}})
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion"}})
		default:
			next.ServeHTTP(w, r)
		}
	})
}
