package privacy

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

func redactURLMatch(raw string, counts map[string]int) string {
	parseRaw := strings.Replace(raw, placeholder("URL_CREDENTIAL"), "%5BREDACTED%3AURL_CREDENTIAL%5D", 1)
	u, err := url.Parse(parseRaw)
	if err != nil || u.Host == "" {
		if sensitiveMalformedURL(raw) {
			counts["MALFORMED_URL"]++
			return placeholder("MALFORMED_URL")
		}
		return raw
	}
	q, queryErr := url.ParseQuery(u.RawQuery)
	if queryErr != nil {
		if sensitiveMalformedURL(raw) {
			counts["MALFORMED_URL"]++
			return placeholder("MALFORMED_URL")
		}
		return raw
	}
	// A later decoded-key failure selects a whole-URL mask. Validate before
	// recording any credential/query edits, so receipts are deterministic and
	// contain only the selected transformation.
	for key := range q {
		if _, err := decodeURLComponent(key); err != nil {
			counts["MALFORMED_URL"]++
			return placeholder("MALFORMED_URL")
		}
	}
	changed := false
	if u.User != nil {
		_, hasPassword := u.User.Password()
		if !exactPlaceholder(u.User.Username(), "URL_CREDENTIAL") || hasPassword {
			u.User = url.User(placeholder("URL_CREDENTIAL"))
			counts["URL_CREDENTIAL"]++
			changed = true
		}
	}
	for key, vals := range q {
		decodedKey, _ := decodeURLComponent(key) // Validated before any counts.
		if sensitiveQueryKey(decodedKey) {
			if len(vals) != 1 || !exactPlaceholder(vals[0], "URL_QUERY") {
				for _, v := range vals {
					if !exactPlaceholder(v, "URL_QUERY") {
						counts["URL_QUERY"]++
					}
				}
				q.Set(key, placeholder("URL_QUERY"))
				changed = true
			}
		}
	}
	if changed {
		u.RawQuery = q.Encode()
		out := strings.ReplaceAll(u.String(), "%5BREDACTED%3AURL_CREDENTIAL%5D", placeholder("URL_CREDENTIAL"))
		return strings.ReplaceAll(out, "%5BREDACTED%3AURL_QUERY%5D", placeholder("URL_QUERY"))
	}
	return raw
}

func redactAuthorizationMatch(match string, counts map[string]int) string {
	colon := strings.IndexByte(match, ':')
	if colon >= 0 && exactPlaceholder(strings.TrimSpace(match[colon+1:]), "AUTHORIZATION") {
		return match
	}
	counts["AUTHORIZATION"]++
	return placeholder("AUTHORIZATION")
}
func redactQuotedMatch(match string, re *regexp.Regexp, json bool, counts map[string]int) string {
	parts := re.FindStringSubmatch(match)
	if len(parts) != 4 {
		return match
	}
	rule := labelRule(parts[1])
	if exactPlaceholder(parts[3], rule) {
		return match
	}
	counts[rule]++
	if json {
		return "\"" + parts[1] + "\"" + parts[2] + "\"" + placeholder(rule) + "\""
	}
	return parts[1] + parts[2] + "\"" + placeholder(rule) + "\""
}
func redactLabelMatch(match string, counts map[string]int) string {
	return redactLabelMatchUsing(match, label, counts)
}
func redactLabelMatchUsing(match string, re *regexp.Regexp, counts map[string]int) string {
	parts := re.FindStringSubmatch(match)
	if len(parts) != 4 {
		return match
	}
	rule := labelRule(parts[1])
	if exactPlaceholder(strings.TrimSpace(parts[3]), rule) {
		return match
	}
	counts[rule]++
	return parts[1] + parts[2] + placeholder(rule)
}
func sortedCounts(counts map[string]int) []Count {
	rules := make([]string, 0, len(counts))
	for rule := range counts {
		rules = append(rules, rule)
	}
	sort.Strings(rules)
	out := make([]Count, 0, len(rules))
	for _, rule := range rules {
		out = append(out, Count{Rule: rule, Count: counts[rule]})
	}
	return out
}
