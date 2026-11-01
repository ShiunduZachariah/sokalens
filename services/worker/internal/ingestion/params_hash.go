package ingestion

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
)

func hashParams(values url.Values) string {
	encoded := values.Encode()
	sum := sha256.Sum256([]byte(encoded))
	return hex.EncodeToString(sum[:])
}
