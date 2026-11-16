package client

import "github.com/hazcod/blur/internal/search"

type memoryFilter struct{ app, domain string }

// Values within a dimension are alternatives. App and website dimensions
// intersect: "Safari on example.com" must satisfy both qualifiers.
func memoryFilters(apps, domains []string) ([]memoryFilter, error) {
	if len(apps)+len(domains) > 8 {
		return nil, ErrInvalidRequest
	}
	canonicalApps := []string{""}
	canonicalDomains := []string{""}
	if len(apps) > 0 {
		canonicalApps = nil
		for _, value := range apps {
			app, err := search.CanonicalAppID(value)
			if err != nil || app == "" {
				return nil, ErrInvalidRequest
			}
			canonicalApps = append(canonicalApps, app)
		}
	}
	if len(domains) > 0 {
		canonicalDomains = nil
		for _, value := range domains {
			domain, err := search.CanonicalDomain(value)
			if err != nil || domain == "" {
				return nil, ErrInvalidRequest
			}
			canonicalDomains = append(canonicalDomains, domain)
		}
	}
	filters := make([]memoryFilter, 0, len(canonicalApps)*len(canonicalDomains))
	for _, app := range canonicalApps {
		for _, domain := range canonicalDomains {
			filters = append(filters, memoryFilter{app: app, domain: domain})
		}
	}
	return filters, nil
}
