package seourl

import (
	"encoding/json"
	"strings"
)

func jsArray(values []string) string {
	encoded, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func decodeJSONStrings(raw []byte, into *[]string) error {
	return json.Unmarshal([]byte(strings.TrimSpace(string(raw))), into)
}
