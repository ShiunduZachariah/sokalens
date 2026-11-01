-- The crawlable URL set, as data rather than as three opinions.
--
-- Indexability was computed independently in three places and they disagreed
-- three times in three weeks: the page archived on an hour-granular age while
-- the sitemap advertised a whole-day window, the sitemap inherited a UI page
-- cap and advertised 2,307 fixtures when 7,323 were indexable, and root and
-- /en/ duplicated each other until #96. None of those produced an error. A
-- wrong crawlable set fails silently and reports back weeks later as a traffic
-- decline nobody can attribute.
--
-- This table is the one answer. The sitemaps select from it, the page reads its
-- own row for the robots directive, and hub pages link only to indexable rows.
-- Three consumers, one definition, so they cannot drift.

CREATE TABLE IF NOT EXISTS seo_urls (
  -- The canonical path, without host or locale prefix. Primary key because a
  -- path is the identity here: two rows for one path is precisely the
  -- duplication this exists to prevent.
  path TEXT PRIMARY KEY,

  entity_type TEXT NOT NULL CHECK (entity_type IN (
    'fixture', 'team', 'league', 'country', 'market', 'date_hub', 'static'
  )),
  entity_id BIGINT,

  -- volatile separates the two populations, because they age completely
  -- differently and a sweep that treats them alike wastes most of its work.
  --
  -- A fixture is volatile: it appears, gains a prediction, is played, and
  -- fourteen days later stops deserving a place in the index. Four states in
  -- three weeks, and the last transition is driven by nothing but the clock.
  --
  -- A team, league or country is stable. Manchester United will still be
  -- Manchester United next season and the season after; the row's indexability
  -- turns on whether the club has content, not on what time it is. Re-deriving
  -- 13,763 of those every few minutes to discover that none of them changed is
  -- the kind of work that quietly becomes the reason the sweep gets disabled.
  volatile BOOLEAN NOT NULL,

  indexable BOOLEAN NOT NULL,

  -- Why a path is withheld, so "why is this page not in Google?" is one query
  -- rather than a bisect through three codebases.
  excluded_reason TEXT CHECK (excluded_reason IS NULL OR excluded_reason IN (
    'no_prediction', 'archived', 'not_played', 'thin', 'duplicate', 'retired'
  )),

  -- lastmod for the sitemap. Deliberately nullable: a lastmod that changes on
  -- every rebuild teaches Google to stop trusting the field across the whole
  -- property, so a path with no real modification time carries none.
  last_modified TIMESTAMPTZ,

  -- When the maintaining sweep last confirmed this row. A row whose entity has
  -- disappeared stops being confirmed, which is how the sweep finds paths to
  -- retire without needing a delete event it may never receive.
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

  -- An excluded row must say why, and an included row must not pretend to.
  -- Without this the two columns drift and `excluded_reason` becomes
  -- decoration instead of an explanation.
  CONSTRAINT seo_urls_reason_matches_indexable CHECK (
    (indexable AND excluded_reason IS NULL) OR
    (NOT indexable AND excluded_reason IS NOT NULL)
  )
);

-- The sitemap's only query: crawlable paths of one kind, newest first.
-- Partial, because the excluded rows are the majority and are never served --
-- 5,580 of 12,903 fixtures in the window carry no prediction.
CREATE INDEX IF NOT EXISTS idx_seo_urls_crawlable
  ON seo_urls (entity_type, last_modified DESC NULLS LAST)
  WHERE indexable;

-- The sweep's query: what did I not just confirm?
CREATE INDEX IF NOT EXISTS idx_seo_urls_sweep
  ON seo_urls (volatile, last_seen_at);

-- Reverse lookup, for the page asking about its own entity and for retiring
-- every path belonging to a merged duplicate.
CREATE INDEX IF NOT EXISTS idx_seo_urls_entity
  ON seo_urls (entity_type, entity_id)
  WHERE entity_id IS NOT NULL;
