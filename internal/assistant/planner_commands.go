package assistant

// retrievalCommandEnd recognises read-only search imperatives with word
// boundaries. A missing source must not turn "Find <subject>" into a request
// requiring generation. Procedural and explicit web requests keep their own
// route; explicit general mode is still applied by the service afterward.
func retrievalCommandEnd(ws []word) int {
	start := 0
	if startsWith(ws, "please") {
		start++
	}
	if len(ws) > start+1 && (ws[start].s == "can" || ws[start].s == "could" || ws[start].s == "would") && ws[start+1].s == "you" {
		start += 2
	}
	if len(ws) > start && ws[start].s == "please" {
		start++
	}
	if len(ws) <= start+1 {
		return 0
	}
	w := ws[start:]
	for _, prefix := range [][]string{{"find", "out"}, {"find", "a", "way"}, {"find", "me", "a", "way"}} {
		if startsWith(w, prefix...) {
			return 0
		}
	}
	if w[0].s == "search" {
		topic := 1
		if w[topic].s == "the" {
			topic++
		}
		if len(w) > topic {
			switch w[topic].s {
			case "web", "internet", "online":
				return 0
			}
		}
	}
	if w[0].s == "find" || w[0].s == "search" {
		return start + 1
	}
	if len(w) > 2 && (startsWith(w, "look", "up") || startsWith(w, "pull", "up")) {
		return start + 2
	}
	return 0
}
