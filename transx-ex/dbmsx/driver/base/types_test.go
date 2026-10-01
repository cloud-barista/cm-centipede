package base

import "testing"

// =============================================================================
// ProviderName — carried, never acted on
// =============================================================================

// The provider names must match cm-honeybee's spelling exactly: a name resolved
// there is meant to pass through unchanged, so a difference in case or wording
// would silently split one provider into two.
func TestProviderConstants_MatchHoneybeeSpelling(t *testing.T) {
	want := map[string]string{
		"aws":       ProviderAWS,
		"alibaba":   ProviderAlibaba,
		"tencent":   ProviderTencent,
		"ncp":       ProviderNCP,
		"nhn":       ProviderNHN,
		"ibm":       ProviderIBM,
		"gcp":       ProviderGCP,
		"kt":        ProviderKT,
		"azure":     ProviderAzure,
		"openstack": ProviderOpenStack,
		"onprem":    ProviderOnPrem,
	}
	for spelling, got := range want {
		if got != spelling {
			t.Errorf("provider constant = %q, want %q", got, spelling)
		}
	}
}

func TestIsOnPrem(t *testing.T) {
	cases := []struct {
		provider string
		want     bool
	}{
		{ProviderOnPrem, true},
		// cm-honeybee folds a provider name before testing it, so a value that
		// reached the caller in another case still answers the question.
		{"OnPrem", true},
		{"ONPREM", true},
		{ProviderAWS, false},
		// Unspecified is not a claim of being on-premises.
		{"", false},
	}
	for _, c := range cases {
		loc := DBMSLocation{ProviderName: c.provider}
		if got := loc.IsOnPrem(); got != c.want {
			t.Errorf("IsOnPrem(%q) = %v, want %v", c.provider, got, c.want)
		}
	}
}
