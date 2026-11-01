package acca

import "strings"

// Namespace returns the isolated key prefix used by ACCA serving caches.
// Callers must pass `qa:acca` for local/QA and `acca` for production; this
// helper never flushes or alters unrelated Redis keys.
func Namespace(configured string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return "acca"
	}
	return strings.TrimSuffix(configured, ":")
}

func Key(prefix, kind, id string) string {
	return Namespace(prefix) + ":" + strings.Trim(kind, ":") + ":" + strings.Trim(id, ":")
}
