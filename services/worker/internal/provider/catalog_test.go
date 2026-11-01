package provider

import "testing"

func TestEndpointSpecValidateRequiresProviderIndependentFields(t *testing.T) {
	spec := EndpointSpec{
		Key:        EndpointCountries,
		Provider:   "api-football",
		Path:       "countries",
		EntityType: EntityCountry,
		QuotaCost:  1,
	}

	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
}

func TestEndpointSpecValidateRejectsInvalidSpecs(t *testing.T) {
	tests := []struct {
		name string
		spec EndpointSpec
	}{
		{name: "missing key", spec: EndpointSpec{Provider: "api-football", Path: "countries", EntityType: EntityCountry, QuotaCost: 1}},
		{name: "missing provider", spec: EndpointSpec{Key: EndpointCountries, Path: "countries", EntityType: EntityCountry, QuotaCost: 1}},
		{name: "absolute path", spec: EndpointSpec{Key: EndpointCountries, Provider: "api-football", Path: "/countries", EntityType: EntityCountry, QuotaCost: 1}},
		{name: "missing entity", spec: EndpointSpec{Key: EndpointCountries, Provider: "api-football", Path: "countries", QuotaCost: 1}},
		{name: "zero quota", spec: EndpointSpec{Key: EndpointCountries, Provider: "api-football", Path: "countries", EntityType: EntityCountry}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.spec.Validate(); err == nil {
				t.Fatal("Validate returned nil error")
			}
		})
	}
}
