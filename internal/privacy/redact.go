package privacy

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const Version = 1

type Count struct {
	Rule  string `json:"rule"`
	Count int    `json:"count"`
}

var (
	pem    = regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z0-9 ]* )?PRIVATE KEY-----.*?-----END (?:[A-Z0-9 ]* )?PRIVATE KEY-----`)
	auth   = regexp.MustCompile(`(?im)\bAuthorization\s*:\s*[^\r\n]+`)
	webURL = regexp.MustCompile(`https?://[^\s<>"']+`)
	label  = regexp.MustCompile(`(?i)\b(password|secret|token|api[_ -]?key|cvv|cvc|security[_ -]?code)\b(\s*[:=]\s*)([^\s,;&]+)`)
	token  = regexp.MustCompile(`\b(?:sk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{20,}|AKIA[A-Z0-9]{16}|ASIA[A-Z0-9]{16}|xox[baprs]-[A-Za-z0-9-]{20,})\b`)
	ssn    = regexp.MustCompile(`\b[0-9]{3}-[0-9]{2}-[0-9]{4}\b`)
	card   = regexp.MustCompile(`\b(?:[0-9][ -]?){12,18}[0-9]\b`)
	iban   = regexp.MustCompile(`\b[A-Z]{2}[0-9]{2}(?:[ ]?[A-Z0-9]){11,30}\b`)
)

func Redact(input string) (string, []Count) {
	counts := map[string]int{}
	replace := func(re *regexp.Regexp, rule string, valid func(string) bool) {
		input = re.ReplaceAllStringFunc(input, func(match string) string {
			if strings.Contains(match, "[REDACTED:") || valid != nil && !valid(match) {
				return match
			}
			counts[rule]++
			return "[REDACTED:" + rule + "]"
		})
	}
	replace(pem, "PRIVATE_KEY", nil)
	replace(auth, "AUTHORIZATION", nil)
	input = webURL.ReplaceAllStringFunc(input, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return raw
		}
		changed := false
		if u.User != nil && u.User.Username() != "[REDACTED:URL_CREDENTIAL]" {
			u.User = url.User("[REDACTED:URL_CREDENTIAL]")
			counts["URL_CREDENTIAL"]++
			changed = true
		}
		q := u.Query()
		for key, vals := range q {
			k := strings.ToLower(key)
			if k == "token" || k == "access_token" || k == "refresh_token" || k == "auth_token" || k == "session_token" || k == "api_key" || k == "apikey" || k == "client_secret" || k == "password" || k == "secret" || k == "auth" || k == "key" {
				if len(vals) > 0 && !strings.HasPrefix(vals[0], "[REDACTED:") {
					for range vals {
						counts["URL_QUERY"]++
					}
					q.Set(key, "[REDACTED:URL_QUERY]")
					changed = true
				}
			}
		}
		if changed {
			u.RawQuery = q.Encode()
			return strings.ReplaceAll(u.String(), "%5BREDACTED%3AURL_CREDENTIAL%5D", "[REDACTED:URL_CREDENTIAL]")
		}
		return raw
	})
	input = label.ReplaceAllStringFunc(input, func(match string) string {
		parts := label.FindStringSubmatch(match)
		if len(parts) != 4 || strings.Contains(parts[3], "REDACTED") {
			return match
		}
		rule := "SECRET"
		if strings.EqualFold(parts[1], "cvv") || strings.EqualFold(parts[1], "cvc") || strings.EqualFold(strings.ReplaceAll(parts[1], "_", " "), "security code") {
			rule = "SECURITY_CODE"
		}
		counts[rule]++
		return parts[1] + parts[2] + "[REDACTED:" + rule + "]"
	})
	replace(token, "TOKEN", nil)
	replace(ssn, "SSN", validSSN)
	replace(card, "CARD", validCard)
	replace(iban, "IBAN", validIBAN)
	rules := make([]string, 0, len(counts))
	for rule := range counts {
		rules = append(rules, rule)
	}
	sort.Strings(rules)
	out := make([]Count, 0, len(rules))
	for _, rule := range rules {
		out = append(out, Count{Rule: rule, Count: counts[rule]})
	}
	return input, out
}

func validSSN(s string) bool {
	return s[:3] != "000" && s[:3] != "666" && s[0] != '9' && s[4:6] != "00" && s[7:] != "0000"
}

func validCard(s string) bool {
	digits := make([]int, 0, len(s))
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits = append(digits, int(r-'0'))
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	allSame := true
	for _, d := range digits[1:] {
		if d != digits[0] {
			allSame = false
			break
		}
	}
	if allSame {
		return false
	}
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := digits[i]
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

func validIBAN(s string) bool {
	s = strings.ReplaceAll(s, " ", "")
	if len(s) < 15 || len(s) > 34 {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) && !(r >= 'A' && r <= 'Z') {
			return false
		}
	}
	shift := s[4:] + s[:4]
	mod := 0
	for _, r := range shift {
		if r >= '0' && r <= '9' {
			mod = (mod*10 + int(r-'0')) % 97
		} else {
			n := int(r-'A') + 10
			mod = (mod*100 + n) % 97
		}
	}
	return mod == 1
}
