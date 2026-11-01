-- provider_request_dedupe: cross-worker lease preventing two workers from
-- sending the identical provider request concurrently. Leases are short-lived
-- (released after the request completes; TTL is a crash-safety net).
CREATE TABLE IF NOT EXISTS provider_request_dedupe (
    fingerprint TEXT PRIMARY KEY,
    claimed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL,
    worker_id   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_provider_request_dedupe_expires
    ON provider_request_dedupe (expires_at);
