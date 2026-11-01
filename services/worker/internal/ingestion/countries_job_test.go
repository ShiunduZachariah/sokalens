package ingestion

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
)

type fakeProviderClient struct {
	endpoint string
	query    url.Values
	response provider.Response
	err      error
	// calls counts provider requests, so a test can assert that a batch cost
	// one request rather than one per item.
	calls int
}

func (f *fakeProviderClient) Get(_ context.Context, endpoint string, query url.Values) (provider.Response, error) {
	f.calls++
	f.endpoint = endpoint
	f.query = cloneValues(query)
	return f.response, f.err
}

func cloneValues(values url.Values) url.Values {
	clone := make(url.Values, len(values))
	for key, vals := range values {
		clone[key] = append([]string(nil), vals...)
	}
	return clone
}

type fakeRequestLogger struct {
	logs []RequestLog
	err  error
}

func (f *fakeRequestLogger) Insert(_ context.Context, log RequestLog) error {
	f.logs = append(f.logs, log)
	return f.err
}

type fakeEntityMapWriter struct {
	maps []ProviderEntityMap
	err  error
}

func (f *fakeEntityMapWriter) Upsert(_ context.Context, mapping ProviderEntityMap) error {
	f.maps = append(f.maps, mapping)
	return f.err
}

type fakeRawPayloadWriter struct {
	payloads []RawPayloadMetadata
	err      error
}

func (f *fakeRawPayloadWriter) InsertMetadata(_ context.Context, payload RawPayloadMetadata) error {
	f.payloads = append(f.payloads, payload)
	return f.err
}

type fakeRateLimitWriter struct {
	updates []ProviderRateLimitUpdate
	err     error
}

func (f *fakeRateLimitWriter) UpdateFromHeaders(_ context.Context, update ProviderRateLimitUpdate) error {
	f.updates = append(f.updates, update)
	return f.err
}

type fakeCountryCatalog struct {
	countries []CountryRecord
	err       error
}

func (f *fakeCountryCatalog) UpsertCountry(_ context.Context, country CountryRecord) error {
	f.countries = append(f.countries, country)
	return f.err
}

func TestCountriesJobLogsResponseAndMapsProviderCountries(t *testing.T) {
	client := &fakeProviderClient{response: provider.Response{
		Provider:      "api-football",
		Endpoint:      "countries",
		StatusCode:    200,
		Body:          []byte(`{"errors":[],"response":[{"name":"Kenya","code":"KE"},{"name":"World","code":null}]}`),
		Latency:       25 * time.Millisecond,
		ResponseBytes: 96,
		RateLimit: provider.RateLimitHeaders{
			DailyLimit:      100,
			DailyRemaining:  99,
			MinuteLimit:     10,
			MinuteRemaining: 9,
		},
	}}
	logs := &fakeRequestLogger{}
	maps := &fakeEntityMapWriter{}
	payloads := &fakeRawPayloadWriter{}
	rateLimits := &fakeRateLimitWriter{}
	catalog := &fakeCountryCatalog{}

	job := NewCountriesJob(CountriesJobOptions{Client: client, RequestLogs: logs, EntityMaps: maps, RawPayloads: payloads, RateLimits: rateLimits, Catalog: catalog})
	if err := job.Run(context.Background(), jobs.Job{}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if client.endpoint != "countries" {
		t.Fatalf("endpoint = %q, want countries", client.endpoint)
	}
	if len(logs.logs) != 1 {
		t.Fatalf("logs = %d, want 1", len(logs.logs))
	}
	if logs.logs[0].StatusCode != 200 || logs.logs[0].Endpoint != "countries" {
		t.Fatalf("log = %+v, want countries 200", logs.logs[0])
	}
	if len(maps.maps) != 2 {
		t.Fatalf("maps = %d, want 2", len(maps.maps))
	}
	if len(payloads.payloads) != 1 {
		t.Fatalf("payload metadata rows = %d, want 1", len(payloads.payloads))
	}
	if len(rateLimits.updates) != 1 {
		t.Fatalf("rate limit updates = %d, want 1", len(rateLimits.updates))
	}
	if len(catalog.countries) != 1 {
		t.Fatalf("catalog countries = %d, want 1", len(catalog.countries))
	}
	if rateLimits.updates[0].DailyRemaining != 99 || rateLimits.updates[0].MinuteRemaining != 9 {
		t.Fatalf("rate limit update = %+v, want free-plan remaining headers", rateLimits.updates[0])
	}
	if payloads.payloads[0].StoragePolicy != "metadata_only" || payloads.payloads[0].ResponseHash == "" {
		t.Fatalf("payload metadata = %+v, want metadata_only with response hash", payloads.payloads[0])
	}
	if maps.maps[0].ProviderEntityID != "KE" {
		t.Fatalf("first map = %+v, want provider entity ID KE", maps.maps[0])
	}
	if maps.maps[1].ProviderEntityID != "name:world" {
		t.Fatalf("second map = %+v, want name fallback", maps.maps[1])
	}
}

func TestCountriesJobLogsProviderErrorsBeforeReturning(t *testing.T) {
	client := &fakeProviderClient{
		response: provider.Response{Provider: "api-football", Endpoint: "countries", StatusCode: 429},
		err:      errors.New("provider returned non-success status"),
	}
	logs := &fakeRequestLogger{}
	maps := &fakeEntityMapWriter{}

	job := NewCountriesJob(CountriesJobOptions{Client: client, RequestLogs: logs, EntityMaps: maps})
	err := job.Run(context.Background(), jobs.Job{})
	if err == nil {
		t.Fatal("Run returned nil error")
	}
	if len(logs.logs) != 1 {
		t.Fatalf("logs = %d, want 1", len(logs.logs))
	}
	if logs.logs[0].ErrorCategory != "provider_status" {
		t.Fatalf("ErrorCategory = %q, want provider_status", logs.logs[0].ErrorCategory)
	}
	if len(maps.maps) != 0 {
		t.Fatalf("maps = %d, want 0", len(maps.maps))
	}
}

func TestCountriesJobRequiresDependencies(t *testing.T) {
	if err := NewCountriesJob(CountriesJobOptions{}).Run(context.Background(), jobs.Job{}); err == nil {
		t.Fatal("Run returned nil error without dependencies")
	}
}

func TestCategorizeProviderErrorRetainsTypedProviderCategory(t *testing.T) {
	err := &apifootball.ProviderError{Category: "plan", Message: "upgrade required"}
	if got := categorizeProviderError(provider.Response{StatusCode: 200}, err); got != "plan" {
		t.Fatalf("category = %q, want plan", got)
	}
}

func TestCategorizeProviderErrorIdentifiesClientSideQuotaBlock(t *testing.T) {
	if got := categorizeProviderError(provider.Response{}, apifootball.ErrDailyQuotaReserved); got != "rate_limit" {
		t.Fatalf("category = %q, want rate_limit", got)
	}
}

func TestProviderQuotaCostDoesNotCountPreflightBlocksAsHTTPCalls(t *testing.T) {
	if got := providerQuotaCost(provider.Response{}); got != 0 {
		t.Fatalf("preflight quota cost = %d, want 0", got)
	}
	if got := providerQuotaCost(provider.Response{StatusCode: 429}); got != 1 {
		t.Fatalf("HTTP response quota cost = %d, want 1", got)
	}
}

func TestCountriesJobMapsKnownSubdivisionsAndSkipsOtherNonISO2Codes(t *testing.T) {
	client := &fakeProviderClient{response: provider.Response{
		Provider:   "api-football",
		Endpoint:   "countries",
		StatusCode: 200,
		Body: []byte(`{"errors":[],"response":[
			{"name":"England","code":"ENG"},
			{"name":"Côte d'Ivoire","code":"CIV"},
			{"name":"World","code":null}
		]}`),
	}}
	logs := &fakeRequestLogger{}
	maps := &fakeEntityMapWriter{}
	catalog := &fakeCountryCatalog{}

	job := NewCountriesJob(CountriesJobOptions{
		Client:      client,
		RequestLogs: logs,
		EntityMaps:  maps,
		Catalog:     catalog,
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 77}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(catalog.countries) != 1 {
		t.Fatalf("catalog countries = %+v, want only mapped England", catalog.countries)
	}
	if got := catalog.countries[0]; got.Code != "GB" || got.Name != "United Kingdom" {
		t.Fatalf("mapped subdivision = %+v, want United Kingdom GB", got)
	}
	if len(maps.maps) != 3 {
		t.Fatalf("provider maps = %d, want all 3 entries retained", len(maps.maps))
	}
	if maps.maps[0].Metadata["canonical_code"] != "GB" {
		t.Fatalf("England metadata = %+v, want canonical_code GB", maps.maps[0].Metadata)
	}
	if len(logs.logs) != 1 || logs.logs[0].JobID != 77 {
		t.Fatalf("logs = %+v, want job id 77", logs.logs)
	}
}
