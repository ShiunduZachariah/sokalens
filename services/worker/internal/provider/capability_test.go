package provider

import (
	"testing"
	"time"
)

func validCapability() Capability {
	observed := time.Date(2026, time.August, 14, 8, 0, 0, 0, time.UTC)
	validUntil := observed.Add(24 * time.Hour)
	return Capability{
		Provider:       "paid-provider",
		Endpoint:       EndpointFixtures,
		EntityType:     EntityMatch,
		ObservedAt:     observed,
		ValidUntil:     &validUntil,
		QuotaCost:      1,
		Plan:           "professional",
		PlanSupport:    PlanSupportSupported,
		Licensing:      Licensing{MayPersistRaw: true, MayUseForTraining: true},
		ContractSource: "provider-plan-snapshot:2026-08-14",
	}
}

func TestCapabilityValidateAndUsability(t *testing.T) {
	capability := validCapability()
	if err := capability.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if !capability.IsUsableAt(capability.ObservedAt.Add(time.Hour)) {
		t.Fatal("supported current capability should be usable")
	}
	if capability.IsUsableAt(*capability.ValidUntil) {
		t.Fatal("expired capability must default closed")
	}
}

func TestCapabilityUnknownPlanSupportDefaultsClosed(t *testing.T) {
	capability := validCapability()
	capability.PlanSupport = PlanSupportUnknown
	if capability.IsUsableAt(capability.ObservedAt.Add(time.Hour)) {
		t.Fatal("unknown plan support must not grant provider access")
	}
}

func TestCapabilityValidateRejectsIncompleteOrInvalidContracts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Capability)
	}{
		{name: "provider", mutate: func(c *Capability) { c.Provider = "" }},
		{name: "endpoint", mutate: func(c *Capability) { c.Endpoint = "" }},
		{name: "entity", mutate: func(c *Capability) { c.EntityType = "" }},
		{name: "observed", mutate: func(c *Capability) { c.ObservedAt = time.Time{} }},
		{name: "expiry", mutate: func(c *Capability) { expired := c.ObservedAt; c.ValidUntil = &expired }},
		{name: "quota", mutate: func(c *Capability) { c.QuotaCost = 0 }},
		{name: "plan", mutate: func(c *Capability) { c.Plan = "" }},
		{name: "support", mutate: func(c *Capability) { c.PlanSupport = "maybe" }},
		{name: "source", mutate: func(c *Capability) { c.ContractSource = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capability := validCapability()
			tt.mutate(&capability)
			if err := capability.Validate(); err == nil {
				t.Fatal("Validate returned nil error")
			}
		})
	}
}
