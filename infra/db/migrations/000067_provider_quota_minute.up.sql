-- Request ledger (architecture v2 §12.1).
--
-- provider_request_logs already records one row per provider request, but it is
-- the raw log: it is pruned, and every dashboard question ("what did P0 spend
-- this hour", "is yield falling") becomes a scan over it. This is the rollup
-- those questions read instead. It is written incrementally on the same call
-- that writes the raw log, so it survives the log's retention window.
--
-- Deliberately not partitioned. One row per (provider, minute, class, group)
-- bounds this at roughly 1,440 minutes x a handful of classes x a handful of
-- groups per provider per day -- thousands of rows, not millions. Partitioning
-- it would add maintenance for no gain.

CREATE TABLE IF NOT EXISTS provider_quota_minute (
  provider        TEXT        NOT NULL,
  minute_bucket   TIMESTAMPTZ NOT NULL,
  priority_class  TEXT        NOT NULL,
  endpoint_group  TEXT        NOT NULL,
  request_count   INTEGER     NOT NULL DEFAULT 0,
  error_count     INTEGER     NOT NULL DEFAULT 0,
  bytes_total     BIGINT      NOT NULL DEFAULT 0,
  -- Yield. Attributed after the write completes, not at request time, so this
  -- stays 0 until the ingesting job reports what the response actually landed.
  rows_written    INTEGER     NOT NULL DEFAULT 0,
  PRIMARY KEY (provider, minute_bucket, priority_class, endpoint_group)
);

-- The burn-down and projected-end-of-day views read a contiguous span of one
-- provider's day. The primary key already leads with provider, but it orders
-- ascending and every dashboard query wants the recent end first.
CREATE INDEX IF NOT EXISTS idx_provider_quota_minute_recent
  ON provider_quota_minute (provider, minute_bucket DESC);

-- Spend-by-class stacked over a day, without scanning groups that are not
-- being asked about.
CREATE INDEX IF NOT EXISTS idx_provider_quota_minute_class
  ON provider_quota_minute (provider, priority_class, minute_bucket DESC);
