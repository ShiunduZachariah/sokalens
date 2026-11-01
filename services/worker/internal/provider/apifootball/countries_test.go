package apifootball

import (
	"os"
	"strings"
	"testing"
)

func TestParseCountriesFromSavedFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/countries.success.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	countries, err := ParseCountries(body)
	if err != nil {
		t.Fatalf("ParseCountries returned error: %v", err)
	}
	if len(countries) != 2 {
		t.Fatalf("len(countries) = %d, want 2", len(countries))
	}
	if countries[0].Name != "Kenya" || countries[0].Code != "KE" {
		t.Fatalf("first country = %+v, want Kenya KE", countries[0])
	}
}

func TestParseCountriesRejectsProviderErrors(t *testing.T) {
	_, err := ParseCountries([]byte(`{"errors":{"token":"invalid api key"},"response":[]}`))
	if err == nil {
		t.Fatal("ParseCountries returned nil error for provider error envelope")
	}
	if !strings.Contains(err.Error(), "provider returned errors") {
		t.Fatalf("error = %v, want provider returned errors", err)
	}
}

func TestParseCountriesAllowsMissingCodeFromProvider(t *testing.T) {
	countries, err := ParseCountries([]byte(`{"errors":[],"response":[{"name":"World","code":null,"flag":null}]}`))
	if err != nil {
		t.Fatalf("ParseCountries returned error: %v", err)
	}
	if len(countries) != 1 {
		t.Fatalf("len(countries) = %d, want 1", len(countries))
	}
	if countries[0].Name != "World" || countries[0].Code != "" {
		t.Fatalf("country = %+v, want World with blank code", countries[0])
	}
}

func TestParseCountriesRejectsInvalidJSON(t *testing.T) {
	_, err := ParseCountries([]byte(`{`))
	if err == nil {
		t.Fatal("ParseCountries returned nil error for invalid JSON")
	}
}
