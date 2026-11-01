package ingestion

import "testing"

func TestProviderJobKeyBuildsProviderScopedQueueKeys(t *testing.T) {
	if got := ProviderJobKey("api-football", "fixtures"); got != "provider.api-football.fixtures" {
		t.Fatalf("ProviderJobKey = %q, want provider.api-football.fixtures", got)
	}
	if got := ProviderJobKey("other-provider", "odds.pre_match"); got != "provider.other-provider.odds.pre_match" {
		t.Fatalf("ProviderJobKey = %q, want provider.other-provider.odds.pre_match", got)
	}
}

func TestProviderIdempotencyKeyBuildsStableProviderEndpointKeys(t *testing.T) {
	if got := ProviderIdempotencyKey("api-football", "fixtures", `{"date":"2026-07-02"}`); got != `api-football:fixtures:{"date":"2026-07-02"}` {
		t.Fatalf("ProviderIdempotencyKey = %q", got)
	}
	if got := ProviderIdempotencyKey("other-provider", "countries", "{}"); got != "other-provider:countries:{}" {
		t.Fatalf("ProviderIdempotencyKey = %q", got)
	}
}

func TestProviderKeysRejectBlankParts(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider string
		endpoint string
	}{
		{name: "missing provider", endpoint: "fixtures"},
		{name: "missing endpoint", provider: "api-football"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ProviderJobKey(test.provider, test.endpoint); got != "" {
				t.Fatalf("ProviderJobKey = %q, want empty", got)
			}
			if got := ProviderIdempotencyKey(test.provider, test.endpoint, "{}"); got != "" {
				t.Fatalf("ProviderIdempotencyKey = %q, want empty", got)
			}
		})
	}
}
