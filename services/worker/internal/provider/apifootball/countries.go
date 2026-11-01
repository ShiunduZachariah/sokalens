package apifootball

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Country struct {
	Name string
	Code string
	Flag string
}

type countriesEnvelope struct {
	Errors   json.RawMessage `json:"errors"`
	Response []struct {
		Name string `json:"name"`
		Code string `json:"code"`
		Flag string `json:"flag"`
	} `json:"response"`
}

func ParseCountries(body []byte) ([]Country, error) {
	var envelope countriesEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	if hasProviderErrors(envelope.Errors) {
		return nil, errors.New("provider returned errors")
	}
	countries := make([]Country, 0, len(envelope.Response))
	for index, item := range envelope.Response {
		country := Country{
			Name: strings.TrimSpace(item.Name),
			Code: strings.ToUpper(strings.TrimSpace(item.Code)),
			Flag: strings.TrimSpace(item.Flag),
		}
		if country.Name == "" {
			return nil, fmt.Errorf("country name is required at index %d", index)
		}
		countries = append(countries, country)
	}
	return countries, nil
}

func hasProviderErrors(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "[]" || trimmed == "{}" || trimmed == "null" {
		return false
	}
	return true
}
