package assistant

import (
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/hazcod/blur/internal/search"
)

// PlanSearch uses the same scope grammar as Ask. Search is always a read:
// short topic phrases do not need to declare a personal-memory intent.
func PlanSearch(question string, now time.Time, apps []KnownApp) (Plan, error) {
	return planQuestion(question, now, apps, true)
}

// Mask quoted spans and code/file tokens before interpreting scope words.
// Their normalized terms are restored afterwards, including words such as
// "today" which ordinary question grammar would consume.
func searchGrammar(question string) (string, []string, error) {
	masked := []byte(question)
	var literal []string
	protect := func(start, end int) error {
		terms, err := search.NormalizeTokens(question[start:end], search.MaxTerms)
		if err != nil || len(terms) == 0 {
			return ErrContext
		}
		literal = append(literal, terms...)
		for i := start; i < end; i++ {
			masked[i] = ' '
		}
		return nil
	}
	for i := 0; i < len(question); i++ {
		quote := question[i]
		if quote != '"' && quote != '`' && (quote != '\'' || i != 0 && question[i-1] != ' ') {
			continue
		}
		for end := i + 1; end < len(question); end++ {
			if question[end] == '\\' {
				end++
				continue
			}
			if question[end] == quote {
				if err := protect(i, end+1); err != nil {
					return "", nil, err
				}
				i = end
				break
			}
		}
	}
	for start := 0; start < len(masked); {
		for start < len(masked) && masked[start] <= ' ' {
			start++
		}
		end := start
		for end < len(masked) && masked[end] > ' ' {
			end++
		}
		field := strings.Trim(string(masked[start:end]), "!?.,\"'")
		// An explicit web URL remains a site qualifier. Punctuation in code
		// identifiers and paths instead belongs to the literal topic.
		lower := strings.ToLower(field)
		if strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") {
			if parsed, err := url.Parse(field); err == nil {
				if domain, err := search.CanonicalDomain(parsed.Hostname()); err == nil && domain != "" {
					for i := start; i < end; i++ {
						masked[i] = ' '
					}
					copy(masked[start:end], domain)
					start = end
					continue
				}
			}
		}
		technical := strings.ContainsAny(field, "_/\\=()[]{};:")
		if dot := strings.LastIndexByte(field, '.'); dot >= 0 {
			_, file := fileExtensions[strings.ToLower(field[dot+1:])]
			technical = technical || file
			// Exported dotted members (context.Canceled, time.Now) are code.
			// Bare lowercase names remain domains; backticks disambiguate others.
			for _, r := range field[dot+1:] {
				technical = technical || unicode.IsUpper(r)
			}
		}
		if technical {
			if err := protect(start, end); err != nil {
				return "", nil, err
			}
		}
		start = end
	}
	return string(masked), literal, nil
}

// A weekday embedded in a domain is a name, not a date expression.
func searchScopeWords(grammar string, words []word) []word {
	var result []word
	for _, w := range words {
		if strings.TrimSpace(grammar[w.start:w.end]) == "" {
			continue
		}
		start, end := w.start, w.end
		for start > 0 && grammar[start-1] > ' ' {
			start--
		}
		for end < len(grammar) && grammar[end] > ' ' {
			end++
		}
		if strings.Contains(strings.Trim(grammar[start:end], "!?.,:;()[]{}\"'<>"), ".") {
			continue
		}
		result = append(result, w)
	}
	return result
}

func searchPlanTerms(terms, literal []string, words []word) ([]string, error) {
	reading := hasSeq(words, "did", "i", "read") || hasSeq(words, "did", "i", "write") || hasSeq(words, "was", "i", "reading") || hasSeq(words, "was", "i", "working")
	seen := map[string]bool{}
	for _, term := range terms {
		if reading {
			switch term {
			case "read", "write", "reading", "writing", "working":
				continue
			}
		}
		seen[term] = true
	}
	for _, term := range literal {
		seen[term] = true
	}
	result := make([]string, 0, len(seen))
	for term := range seen {
		result = append(result, term)
	}
	sort.Strings(result)
	if len(result) > search.MaxTerms {
		return nil, ErrContext
	}
	return result, nil
}
