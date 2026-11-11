package macospackage

import "testing"

func TestSelectSigningIdentityPreservesExistingBundleSigner(t *testing.T) {
	identities := `  1) FE6347055F659A7534B1BF8BB30BD45520CA3839 "Apple Development: developer@example.com (A1B2C3D4E5)"
  2) B04D9559825E5E9AE61A80052BB3C9BB3CEC5C36 "Apple Development: Niels Hofmans (Y29TQ7HD5D)"
  3) B04D9559825E5E9AE61A80052BB3C9BB3CEC5C36 "Apple Development: Niels Hofmans (Y29TQ7HD5D)"
     3 valid identities found
`
	signature := `Authority=Apple Development: developer@example.com (A1B2C3D4E5)
Authority=Apple Worldwide Developer Relations Certification Authority
TeamIdentifier=Z9Y8X7W6V5
`
	if got := SelectSigningIdentity(identities, signature); got != "FE6347055F659A7534B1BF8BB30BD45520CA3839" {
		t.Fatalf("selected identity = %q, want existing app signer", got)
	}
}

func TestSelectSigningIdentityUsesOnlyUnambiguousIdentity(t *testing.T) {
	tests := []struct {
		name, identities, signature, want string
	}{
		{"only identity", `  1) ABCDEF0123456789ABCDEF0123456789ABCDEF01 "Developer ID Application: Builder (TEAM123)"`, "", "ABCDEF0123456789ABCDEF0123456789ABCDEF01"},
		{"ambiguous identities", `  1) ABCDEF0123456789ABCDEF0123456789ABCDEF01 "Developer ID Application: One (TEAM123)"
  2) ABCDEF0123456789ABCDEF0123456789ABCDEF02 "Developer ID Application: Two (TEAM456)"`, "", ""},
		{"unmatched existing signer", `  1) ABCDEF0123456789ABCDEF0123456789ABCDEF01 "Developer ID Application: One (TEAM123)"
  2) ABCDEF0123456789ABCDEF0123456789ABCDEF02 "Developer ID Application: Two (TEAM456)"`, "Authority=Developer ID Application: Other (TEAM789)\nTeamIdentifier=TEAM789", ""},
		{"duplicate signer names", `  1) ABCDEF0123456789ABCDEF0123456789ABCDEF01 "Developer ID Application: Builder (TEAM123)"
  2) ABCDEF0123456789ABCDEF0123456789ABCDEF02 "Developer ID Application: Builder (TEAM123)"`, "Authority=Developer ID Application: Builder (TEAM123)\nTeamIdentifier=TEAM123", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SelectSigningIdentity(tt.identities, tt.signature); got != tt.want {
				t.Fatalf("selected identity = %q, want %q", got, tt.want)
			}
		})
	}
}
