package locales

import (
	"strconv"
	"strings"
)

const Default = "en"

type Definition struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Direction string `json:"direction"`
}

var supported = []Definition{
	{Code: "en", Name: "English", Direction: "ltr"},
	{Code: "es", Name: "Español", Direction: "ltr"},
	{Code: "fr", Name: "Français", Direction: "ltr"},
	{Code: "pt", Name: "Português", Direction: "ltr"},
	{Code: "ar", Name: "العربية", Direction: "rtl"},
	{Code: "de", Name: "Deutsch", Direction: "ltr"},
	{Code: "it", Name: "Italiano", Direction: "ltr"},
}

// countryDefaults are only a final detection hint. A selected, account, guest,
// or browser language always takes precedence.
var countryDefaults = map[string]string{
	"AE": "ar", "AR": "es", "AT": "de", "BR": "pt", "CH": "de", "CL": "es",
	"CO": "es", "DE": "de", "DZ": "ar", "EC": "es", "EG": "ar", "ES": "es",
	"FR": "fr", "IT": "it", "MA": "ar", "MX": "es", "PE": "es", "PT": "pt",
	"QA": "ar", "SA": "ar", "TN": "ar", "UY": "es", "VE": "es",
}

func Supported() []Definition {
	result := make([]Definition, len(supported))
	copy(result, supported)
	return result
}

func Normalize(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if separator := strings.IndexAny(value, "-_"); separator >= 0 {
		value = value[:separator]
	}
	for _, definition := range supported {
		if value == definition.Code {
			return value, true
		}
	}
	return "", false
}

type Inputs struct {
	Selected    string
	Saved       string
	Guest       string
	Browser     string
	CountryCode string
}

type Resolution struct {
	Locale    string `json:"locale"`
	Direction string `json:"direction"`
	Source    string `json:"source"`
}

func Resolve(input Inputs) Resolution {
	for _, candidate := range []struct {
		value  string
		source string
	}{
		{input.Selected, "selected"},
		{input.Saved, "account"},
		{input.Guest, "guest"},
		{firstSupportedLanguage(input.Browser), "browser"},
	} {
		if locale, ok := Normalize(candidate.value); ok {
			return Resolution{Locale: locale, Direction: Direction(locale), Source: candidate.source}
		}
	}
	if locale, ok := countryDefaults[strings.ToUpper(strings.TrimSpace(input.CountryCode))]; ok {
		return Resolution{Locale: locale, Direction: Direction(locale), Source: "country"}
	}
	return Resolution{Locale: Default, Direction: "ltr", Source: "fallback"}
}

func Direction(locale string) string {
	if normalized, ok := Normalize(locale); ok && normalized == "ar" {
		return "rtl"
	}
	return "ltr"
}

func firstSupportedLanguage(header string) string {
	if len(header) > 512 {
		header = header[:512]
	}
	bestLocale := ""
	bestQuality := -1.0
	for index, part := range strings.Split(header, ",") {
		if index >= 20 {
			break
		}
		segments := strings.Split(part, ";")
		quality := 1.0
		for _, parameter := range segments[1:] {
			keyValue := strings.SplitN(strings.TrimSpace(parameter), "=", 2)
			if len(keyValue) == 2 && strings.EqualFold(keyValue[0], "q") {
				parsed, err := strconv.ParseFloat(keyValue[1], 64)
				if err != nil || parsed < 0 || parsed > 1 {
					quality = 0
				} else {
					quality = parsed
				}
			}
		}
		if quality <= 0 {
			continue
		}
		candidate, ok := Normalize(strings.TrimSpace(segments[0]))
		if ok && quality > bestQuality {
			bestLocale = candidate
			bestQuality = quality
		}
	}
	return bestLocale
}
