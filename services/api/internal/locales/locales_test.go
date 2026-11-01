package locales

import "testing"

func TestResolveUsesUserChoiceBeforeHints(t *testing.T) {
	got := Resolve(Inputs{Selected: "fr-FR", Saved: "de", Guest: "es", Browser: "pt-BR", CountryCode: "IT"})
	if got.Locale != "fr" || got.Source != "selected" || got.Direction != "ltr" {
		t.Fatalf("resolution=%+v", got)
	}
}

func TestResolveOrderAndFallback(t *testing.T) {
	tests := []struct {
		name, locale, source string
		input                Inputs
	}{
		{"account", "de", "account", Inputs{Saved: "de", Guest: "es", Browser: "pt-BR", CountryCode: "IT"}},
		{"guest", "es", "guest", Inputs{Guest: "es", Browser: "pt-BR", CountryCode: "IT"}},
		{"browser", "ar", "browser", Inputs{Browser: "fr;q=0, ja, ar;q=0.8", CountryCode: "IT"}},
		{"country hint", "pt", "country", Inputs{CountryCode: "BR"}},
		{"fallback", "en", "fallback", Inputs{Browser: "ja", CountryCode: "JP"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Resolve(test.input)
			if got.Locale != test.locale || got.Source != test.source {
				t.Fatalf("resolution=%+v", got)
			}
		})
	}
}

func TestBrowserLanguageHonorsQualityWeights(t *testing.T) {
	got := Resolve(Inputs{Browser: "es;q=0.2, fr-FR;q=0.9, ar;q=0"})
	if got.Locale != "fr" || got.Source != "browser" {
		t.Fatalf("resolution=%+v", got)
	}
}

func TestArabicIsRTLAndSupportedIsDefensiveCopy(t *testing.T) {
	if Direction("ar-SA") != "rtl" {
		t.Fatal("Arabic was not RTL")
	}
	definitions := Supported()
	definitions[0].Code = "changed"
	if Supported()[0].Code != "en" {
		t.Fatal("supported locale definitions shared mutable state")
	}
}
