// Package browserprivacy contains conservative classifiers for browser
// privacy-mode markers exposed by native accessibility window titles.
package browserprivacy

import (
	"strings"
	"unicode"
)

// IsPrivateWindowTitle returns true for empty titles and recognized localized
// private-mode labels. Callers must fail closed when the OS cannot read or
// validate a browser window title.
func IsPrivateWindowTitle(title string) bool {
	normalized := strings.ToLower(strings.TrimSpace(title))
	if normalized == "" {
		return true
	}
	normalized = strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) || unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, normalized)
	for _, marker := range privateBrowserMarkers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	if strings.Contains(normalized, " privat ") || strings.HasPrefix(normalized, "privat ") ||
		strings.Contains(normalized, " privates ") || strings.Contains(normalized, " privater ") {
		return true
	}
	return false
}

var privateBrowserMarkers = []string{
	"private browsing", "private window", "incognito", "inprivate",
	"privates fenster", "privaten fenster", "privat browsing", "privat fönster",
	"privater modus", "privatno pregledavanje", "tryb prywatny", "shfletim privat",
	"privat nettlesing", "privat läge", "privat lage", "privatno pretraživanje",
	"privatno brskanje", "privatno okno", "navigation privée", "navigation privee",
	"navigation priv", "navigare privat", "navigazione anonima", "nabigatze pribatua",
	"yksityinen selaus", "pelayaran peribadi", "anonymn",
	"navegación privada", "navegacion privada", "navegação privada", "navegacao privada",
	"navegación de incógnito", "navigazione privata", "navigazione anonima",
	"navegación anónima", "navegacion anonima", "privé", "prive",
	"private navigator", "privat surfen", "privat nettlesing", "prywatne",
	"privat navigering", "privatni način", "privatni nacin", "privado",
	"anonime fenster", "anonymes fenster", "anonym surfen", "gizli pencere", "gizli gezinti",
	"gizli gezinme", "özel gezinme", "ozel gezinme", "privat fönster", "huli pilikino",
}
