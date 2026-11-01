package paidmock

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
)

var fixtureNames = map[Scenario]string{
	ScenarioSuccess:       "fixtures.success.json",
	ScenarioProviderError: "fixtures.provider-error.json",
	ScenarioRateLimit:     "fixtures.rate-limit.json",
	ScenarioSchemaChange:  "fixtures.schema-change.json",
}

type fixture struct {
	RecordedAt     time.Time         `json:"recorded_at"`
	Capability     fixtureCapability `json:"capability"`
	Request        fixtureRequest    `json:"request"`
	Response       fixtureResponse   `json:"response"`
	ExpectedSchema string            `json:"expected_schema"`
	ObservedSchema string            `json:"observed_schema"`
}

type fixtureCapability struct {
	Provider       string               `json:"provider"`
	Endpoint       provider.EndpointKey `json:"endpoint"`
	EntityType     provider.EntityType  `json:"entity_type"`
	ObservedAt     time.Time            `json:"observed_at"`
	ValidUntil     *time.Time           `json:"valid_until"`
	QuotaCost      int                  `json:"quota_cost"`
	Plan           string               `json:"plan"`
	PlanSupport    provider.PlanSupport `json:"plan_support"`
	Licensing      fixtureLicensing     `json:"licensing"`
	ContractSource string               `json:"contract_source"`
	Notes          string               `json:"notes"`
}

// Pointer booleans distinguish an explicitly denied use from omitted license
// metadata. Every right must be recorded; missing rights fail fixture loading.
type fixtureLicensing struct {
	MayPersistRaw       *bool `json:"may_persist_raw"`
	MayUseForTraining   *bool `json:"may_use_for_training"`
	MayServePublicly    *bool `json:"may_serve_publicly"`
	MayServeToPaidUsers *bool `json:"may_serve_to_paid_users"`
}

type fixtureRequest struct {
	Endpoint string     `json:"endpoint"`
	Query    url.Values `json:"query"`
}

type fixtureResponse struct {
	StatusCode int               `json:"status_code"`
	LatencyMS  int               `json:"latency_ms"`
	Headers    map[string]string `json:"headers"`
	Body       json.RawMessage   `json:"body"`
}

func (f fixture) validate() error {
	if f.RecordedAt.IsZero() {
		return errors.New("recorded_at is required")
	}
	capability, err := f.Capability.validate()
	if err != nil {
		return err
	}
	if capability.Provider != ProviderName {
		return fmt.Errorf("capability provider must be %q", ProviderName)
	}
	if !capability.IsUsableAt(f.RecordedAt) {
		return errors.New("capability must be supported and current at recorded_at")
	}
	if strings.TrimSpace(f.Request.Endpoint) == "" {
		return errors.New("request endpoint is required")
	}
	if f.Request.Endpoint != string(capability.Endpoint) {
		return errors.New("request endpoint must match capability endpoint")
	}
	if f.Response.StatusCode < 100 || f.Response.StatusCode > 599 {
		return errors.New("response status_code must be a valid HTTP status")
	}
	if f.Response.LatencyMS < 0 {
		return errors.New("response latency_ms cannot be negative")
	}
	if len(f.Response.Body) == 0 {
		return errors.New("response body is required")
	}
	if strings.TrimSpace(f.ExpectedSchema) == "" || strings.TrimSpace(f.ObservedSchema) == "" {
		return errors.New("expected_schema and observed_schema are required")
	}
	return nil
}

func (c fixtureCapability) validate() (provider.Capability, error) {
	if c.Licensing.MayPersistRaw == nil || c.Licensing.MayUseForTraining == nil ||
		c.Licensing.MayServePublicly == nil || c.Licensing.MayServeToPaidUsers == nil {
		return provider.Capability{}, errors.New("all licensing flags must be explicitly recorded")
	}
	capability := c.toProvider()
	if err := capability.Validate(); err != nil {
		return provider.Capability{}, fmt.Errorf("invalid capability: %w", err)
	}
	return capability, nil
}

func (c fixtureCapability) toProvider() provider.Capability {
	return provider.Capability{
		Provider:    c.Provider,
		Endpoint:    c.Endpoint,
		EntityType:  c.EntityType,
		ObservedAt:  c.ObservedAt,
		ValidUntil:  c.ValidUntil,
		QuotaCost:   c.QuotaCost,
		Plan:        c.Plan,
		PlanSupport: c.PlanSupport,
		Licensing: provider.Licensing{
			MayPersistRaw:       boolValue(c.Licensing.MayPersistRaw),
			MayUseForTraining:   boolValue(c.Licensing.MayUseForTraining),
			MayServePublicly:    boolValue(c.Licensing.MayServePublicly),
			MayServeToPaidUsers: boolValue(c.Licensing.MayServeToPaidUsers),
		},
		ContractSource: c.ContractSource,
		Notes:          c.Notes,
	}
}

func boolValue(value *bool) bool { return value != nil && *value }
