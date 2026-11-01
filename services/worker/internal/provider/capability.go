package provider

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// PlanSupport describes whether a capability is usable under the configured
// provider plan. Unknown is deliberately distinct from unsupported: callers
// must not assume access until it has been observed or contractually confirmed.
type PlanSupport string

const (
	PlanSupportUnknown     PlanSupport = "unknown"
	PlanSupportSupported   PlanSupport = "supported"
	PlanSupportUnsupported PlanSupport = "unsupported"
)

// Licensing flags describe how provider data may be retained and served. They
// are policy inputs, not legal conclusions; unknown flags must be treated as
// false by downstream publishing and training paths.
type Licensing struct {
	MayPersistRaw       bool `json:"may_persist_raw"`
	MayUseForTraining   bool `json:"may_use_for_training"`
	MayServePublicly    bool `json:"may_serve_publicly"`
	MayServeToPaidUsers bool `json:"may_serve_to_paid_users"`
}

// Capability is the immutable, observed contract for one provider endpoint.
// A newly observed contract should replace the complete value rather than
// mutating individual fields shared with another goroutine.
type Capability struct {
	Provider       string      `json:"provider"`
	Endpoint       EndpointKey `json:"endpoint"`
	EntityType     EntityType  `json:"entity_type"`
	ObservedAt     time.Time   `json:"observed_at"`
	ValidUntil     *time.Time  `json:"valid_until,omitempty"`
	QuotaCost      int         `json:"quota_cost"`
	Plan           string      `json:"plan"`
	PlanSupport    PlanSupport `json:"plan_support"`
	Licensing      Licensing   `json:"licensing"`
	ContractSource string      `json:"contract_source"`
	Notes          string      `json:"notes,omitempty"`
}

func (c Capability) Validate() error {
	if strings.TrimSpace(c.Provider) == "" {
		return errors.New("provider is required")
	}
	if c.Endpoint == "" {
		return errors.New("endpoint is required")
	}
	if c.EntityType == "" {
		return errors.New("entity type is required")
	}
	if c.ObservedAt.IsZero() {
		return errors.New("observed_at is required")
	}
	if c.ValidUntil != nil && !c.ValidUntil.After(c.ObservedAt) {
		return errors.New("valid_until must be after observed_at")
	}
	if c.QuotaCost <= 0 {
		return errors.New("quota_cost must be positive")
	}
	if strings.TrimSpace(c.Plan) == "" {
		return errors.New("plan is required")
	}
	switch c.PlanSupport {
	case PlanSupportUnknown, PlanSupportSupported, PlanSupportUnsupported:
	default:
		return fmt.Errorf("invalid plan_support %q", c.PlanSupport)
	}
	if strings.TrimSpace(c.ContractSource) == "" {
		return errors.New("contract_source is required")
	}
	return nil
}

// IsUsableAt defaults closed when plan support is unknown, the observation is
// stale, or the contract is otherwise invalid.
func (c Capability) IsUsableAt(now time.Time) bool {
	if c.Validate() != nil || c.PlanSupport != PlanSupportSupported {
		return false
	}
	return c.ValidUntil == nil || now.Before(*c.ValidUntil)
}
