// Package paidmock provides a deterministic, fixture-backed paid-provider
// adapter for contract and ingestion tests. It contains no HTTP client or
// network transport and must never be registered as a production provider.
package paidmock

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
)

const ProviderName = "paid-provider-mock"

type Scenario string

const (
	ScenarioSuccess       Scenario = "success"
	ScenarioProviderError Scenario = "provider_error"
	ScenarioRateLimit     Scenario = "rate_limit"
	ScenarioSchemaChange  Scenario = "schema_change"
)

var (
	ErrProviderResponse = errors.New("paid provider returned an error")
	ErrRateLimited      = errors.New("paid provider rate limit exhausted")
	ErrSchemaChanged    = errors.New("paid provider response schema changed")
	ErrRequestMismatch  = errors.New("request does not match recorded fixture")
)

//go:embed testdata/*.json
var recordedFixtures embed.FS

// Adapter replays one immutable provider response. It deliberately has no
// base URL, credentials, HTTP client, or transport, so Get cannot reach the
// network even when invoked by ingestion code.
type Adapter struct {
	fixture fixture
	calls   atomic.Int64
}

func New(scenario Scenario) (*Adapter, error) {
	name, ok := fixtureNames[scenario]
	if !ok {
		return nil, fmt.Errorf("unknown paid-provider mock scenario %q", scenario)
	}

	data, err := recordedFixtures.ReadFile("testdata/" + name)
	if err != nil {
		return nil, fmt.Errorf("read recorded fixture: %w", err)
	}

	var f fixture
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("decode recorded fixture: %w", err)
	}
	if err := f.validate(); err != nil {
		return nil, fmt.Errorf("validate recorded fixture: %w", err)
	}
	return &Adapter{fixture: f}, nil
}

// Capability returns a copy of the recorded, validated endpoint contract.
func (a *Adapter) Capability() provider.Capability {
	return a.fixture.Capability.toProvider()
}

// Calls reports replay attempts. It is useful for asserting retry behavior;
// it is not a network-call counter because this adapter has no network path.
func (a *Adapter) Calls() int64 { return a.calls.Load() }

func (a *Adapter) Get(ctx context.Context, endpoint string, query url.Values) (provider.Response, error) {
	select {
	case <-ctx.Done():
		return provider.Response{Provider: ProviderName, Endpoint: endpoint}, ctx.Err()
	default:
	}

	a.calls.Add(1)
	if endpoint != a.fixture.Request.Endpoint || !equalQuery(query, a.fixture.Request.Query) {
		return provider.Response{Provider: ProviderName, Endpoint: endpoint}, fmt.Errorf(
			"%w: got endpoint=%q query=%q, want endpoint=%q query=%q",
			ErrRequestMismatch, endpoint, query.Encode(), a.fixture.Request.Endpoint, a.fixture.Request.Query.Encode(),
		)
	}

	capability := a.Capability()
	if !capability.IsUsableAt(a.fixture.RecordedAt) {
		return provider.Response{Provider: ProviderName, Endpoint: endpoint}, fmt.Errorf("capability is not usable at recording time: %w", ErrProviderResponse)
	}

	resp := provider.Response{
		Provider:      ProviderName,
		Endpoint:      endpoint,
		StatusCode:    a.fixture.Response.StatusCode,
		Body:          append([]byte(nil), a.fixture.Response.Body...),
		Latency:       time.Duration(a.fixture.Response.LatencyMS) * time.Millisecond,
		ResponseBytes: len(a.fixture.Response.Body),
		RateLimit:     rateLimitFromHeaders(a.fixture.Response.Headers),
	}

	if a.fixture.ExpectedSchema != a.fixture.ObservedSchema {
		return resp, fmt.Errorf("%w: expected %q, observed %q", ErrSchemaChanged, a.fixture.ExpectedSchema, a.fixture.ObservedSchema)
	}
	if resp.StatusCode == 429 {
		return resp, ErrRateLimited
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, fmt.Errorf("%w: status %d", ErrProviderResponse, resp.StatusCode)
	}
	return resp, nil
}

func equalQuery(got, want url.Values) bool {
	if got == nil {
		got = url.Values{}
	}
	return got.Encode() == want.Encode()
}

func rateLimitFromHeaders(headers map[string]string) provider.RateLimitHeaders {
	raw := make(map[string]string, len(headers))
	for key, value := range headers {
		raw[key] = value
	}
	return provider.RateLimitHeaders{
		DailyLimit:      headerInt(headers, "x-ratelimit-requests-limit"),
		DailyRemaining:  headerInt(headers, "x-ratelimit-requests-remaining"),
		MinuteLimit:     headerInt(headers, "x-ratelimit-minute-limit"),
		MinuteRemaining: headerInt(headers, "x-ratelimit-minute-remaining"),
		Raw:             raw,
	}
}

func headerInt(headers map[string]string, name string) int {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			parsed, _ := strconv.Atoi(value)
			return parsed
		}
	}
	return 0
}
