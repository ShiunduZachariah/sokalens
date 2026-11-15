-- The ledger behind automated marketing posts.
--
-- One row per post per channel. The unique key is what makes a retried job
-- safe: the worker writes the row (with the exact caption and card it means to
-- send) before calling the channel, so a crash between "sent" and "recorded"
-- can at worst leave a row in `pending`, never a second post in a public
-- channel. Content is stored on the first insert and reused on every retry, so
-- a retry can never post different numbers from the first attempt.
--
-- It is also the record the results and weekly-record cards are built from:
-- the picks a results card grades are the picks that were posted, read back
-- from `payload`, not whatever the engine publishes now.
--
-- Additive: a new table and its indexes. Nothing existing is touched.
CREATE TABLE IF NOT EXISTS marketing_posts (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  post_key TEXT NOT NULL,
  channel TEXT NOT NULL,
  kind TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  caption TEXT NOT NULL DEFAULT '',
  card_token TEXT NOT NULL DEFAULT '',
  payload JSONB NOT NULL DEFAULT '{}'::jsonb,
  external_id TEXT,
  last_error TEXT,
  attempts INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  sent_at TIMESTAMPTZ,
  CONSTRAINT marketing_posts_post_key_channel_key UNIQUE (post_key, channel),
  CONSTRAINT marketing_posts_status_check CHECK (status IN ('pending', 'sent', 'failed', 'skipped')),
  CONSTRAINT marketing_posts_channel_check CHECK (channel IN ('telegram', 'facebook', 'owner')),
  CONSTRAINT marketing_posts_attempts_check CHECK (attempts >= 0)
);

CREATE INDEX IF NOT EXISTS idx_marketing_posts_kind_created
  ON marketing_posts (kind, created_at DESC);
