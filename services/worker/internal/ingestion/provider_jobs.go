package ingestion

import (
	"fmt"
	"strings"
)

func ProviderJobKey(provider string, endpoint string) string {
	provider = strings.TrimSpace(provider)
	endpoint = strings.TrimSpace(endpoint)
	if provider == "" || endpoint == "" {
		return ""
	}
	return fmt.Sprintf("provider.%s.%s", provider, endpoint)
}

func ProviderIdempotencyKey(provider string, endpoint string, params string) string {
	provider = strings.TrimSpace(provider)
	endpoint = strings.TrimSpace(endpoint)
	params = strings.TrimSpace(params)
	if provider == "" || endpoint == "" {
		return ""
	}
	if params == "" {
		params = "{}"
	}
	return fmt.Sprintf("%s:%s:%s", provider, endpoint, params)
}
