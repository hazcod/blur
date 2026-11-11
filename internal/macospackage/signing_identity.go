package macospackage

import (
	"regexp"
	"strings"
)

var signingIdentityLine = regexp.MustCompile(`^\s*\d+\)\s+([A-Fa-f0-9]{40})\s+"([^"]+)"\s*$`)

type signingIdentity struct {
	hash string
	name string
}

// SelectSigningIdentity returns the existing app's unique signer when that
// identity is still installed, or the sole valid local identity when no
// reliable existing signer is available. Ambiguity deliberately returns an
// empty string so the caller can use its documented ad-hoc fallback.
func SelectSigningIdentity(identityOutput, signatureOutput string) string {
	identities := parseSigningIdentities(identityOutput)
	if len(identities) == 0 {
		return ""
	}
	preferredName, teamID := parseSignatureIdentity(signatureOutput)
	if preferredName != "" && teamID != "" {
		var match string
		for _, identity := range identities {
			// The parenthesized value in Apple's displayed identity name is
			// not necessarily the TeamIdentifier, so match the exact Authority
			// string and require the existing bundle to carry a team identity.
			if identity.name != preferredName {
				continue
			}
			if match != "" && match != identity.hash {
				return ""
			}
			match = identity.hash
		}
		if match != "" {
			return match
		}
	}
	if len(identities) == 1 {
		return identities[0].hash
	}
	return ""
}

func parseSigningIdentities(output string) []signingIdentity {
	byHash := make(map[string]signingIdentity)
	for _, line := range strings.Split(output, "\n") {
		match := signingIdentityLine.FindStringSubmatch(line)
		if len(match) != 3 {
			continue
		}
		hash := strings.ToUpper(match[1])
		if _, exists := byHash[hash]; !exists {
			byHash[hash] = signingIdentity{hash: hash, name: match[2]}
		}
	}
	identities := make([]signingIdentity, 0, len(byHash))
	for _, identity := range byHash {
		identities = append(identities, identity)
	}
	return identities
}

func parseSignatureIdentity(output string) (authority, teamID string) {
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "Authority":
			if authority == "" {
				authority = value
			}
		case "TeamIdentifier":
			teamID = value
		}
	}
	return authority, teamID
}
