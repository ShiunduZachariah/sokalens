# The crawlable URL set, and how it stays correct

## The problem this solves

SokaLens publishes a catalogue that never stops moving. Today there are 51,370
matches, 13,763 teams and 1,241 leagues. Next weekend brings a few hundred more
fixtures; next season brings new teams, new competitions, and retires others.
Five years from now the shape is the same and the volume is larger.

Every one of those entities may or may not deserve a URL in a search index at
any given moment, and the answer changes over the life of a single fixture:

```
created ──► no prediction yet ──► prediction published ──► played ──► 14 days on
   │            noindex               indexable            indexable    archived
   │                                                                    noindex
   └── never played (postponed, cancelled) ─────────────────────────────► noindex
```

So "which URLs should Google crawl?" is not a list anyone can maintain. It is a
derived property of data that changes every few minutes, and the only durable
answer is a mechanism that derives it continuously.

## What went wrong without one

Three defects in three weeks, all the same shape: **two places computed
indexability independently and disagreed.**

**The page and the sitemap disagreed about age.** `FIXTURE_DAYS_BACK` was set
to `SETTLED_INDEXABLE_DAYS`, which looks obviously correct. The sitemap counts
whole days; the page archives on `ageDays > SETTLED_INDEXABLE_DAYS`, counted in
hours. A fixture that kicked off at 11:00 on day -14 is 14.16 days old by the
afternoon, so the page answered `noindex` while the sitemap still advertised
it. Caught 2026-09-21 by the deploy's own gate.

**The sitemap borrowed a UI budget.** It called the homepage's feed loader,
which caps at five pages of 100 so a busy Saturday cannot stampede PostgreSQL.
Correct for a homepage. For a sitemap it meant advertising 2,307 fixtures when
7,323 had current predictions -- about five thousand rankable pages Google was
never told about. Nothing failed. The sitemap was simply smaller than the truth.

**The site duplicated itself.** Root and `/en/` were byte-identical trees, both
self-canonical; `/matches/{id}` and `/predictions/{league}/{slug}-{id}` rendered
the same fixture. Google crawled ~16,930 URLs, declined to index 8,318 of them,
and impressions fell from 928/day to 6/day -- a 99.4% collapse. #96 fixed two of
the three duplications. The third, 705 duplicate team rows, is still open.

None of these produced an error. That is the defining property: a wrong
crawlable set fails silently, and the feedback arrives weeks later as a traffic
decline nobody can attribute.

## Why this is urgent rather than tidy

Googlebot is crawling roughly **275 URLs a day** against a discovered footprint
in the tens of thousands. At that rate a sitewide change takes two months to
propagate -- which is why, two weeks after #96, Google still ranks `/en/` URLs
that now redirect.

Crawl budget is the scarce resource. Every crawl spent on a `noindex` page, a
duplicate, or a redirect is one not spent on a page that could rank. The
platform currently leaks it in three ways at once.

## The mechanism

One table is the source of truth for what may be crawled, maintained by the
worker, and read by everything that needs the answer.

```sql
CREATE TABLE seo_urls (
  path            TEXT PRIMARY KEY,
  entity_type     TEXT NOT NULL,   -- fixture | team | league | market | date_hub | static
  entity_id       BIGINT,
  indexable       BOOLEAN NOT NULL,
  excluded_reason TEXT,            -- no_prediction | archived | duplicate_of | thin
  last_modified   TIMESTAMPTZ,
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX seo_urls_crawlable
  ON seo_urls (entity_type, last_modified DESC)
  WHERE indexable;
```

Three consumers, one definition:

- **The sitemaps** select `WHERE indexable`. No pagination cap, no API round
  trip, no second opinion about age.
- **The page** reads its own row for the `robots` directive, instead of
  recomputing `Boolean(prediction) && !isArchivedFixture(match)`.
- **Hub pages** (league, team, date) link only to rows that are `indexable`.

That third one is what makes `noindex` pages stop being crawled at all. A
`noindex` page that is still linked is still fetched -- the directive is only
discovered *after* the crawl it was meant to save. Removing the link is the only
thing that actually recovers the budget.

`excluded_reason` is not decoration. It is how you answer "why is this page not
in Google?" in one query instead of a bisect through three codebases.

## Keeping it current

A worker job maintains the table. It has to be both event-driven and periodic,
because the two catch different things:

**On events**, because they are the common case and the cheapest signal.
A prediction publishes, so its fixture becomes indexable. A fixture's status
resolves. A team or league is created by ingestion. A duplicate is merged, so
the losing path is retired.

**On a schedule**, because the most important transition has no event at all.
A fixture becomes archived by the passage of time -- nothing happens, no row is
written, no message is sent. Only a periodic sweep notices that yesterday's
indexable page is today's archived one. The same sweep is the reconciliation
pass that repairs whatever the event path dropped, which matters because the
event path will drop things.

This is the same lesson as `matchstate`: a state that changes when nobody is
looking needs a sweeper, not a listener.

## Invariants worth a test each

These are the properties whose violation is silent, so each needs a guard that
fails loudly:

1. **No sitemap URL is `noindex`.** Already enforced post-deploy by
   `search-index-audit.mjs`; it is what caught the age boundary.
2. **Every indexable page is in a sitemap.** The converse, and currently
   unguarded -- it is how five thousand pages went unadvertised.
3. **No hub page links to a `noindex` URL.** The crawl-budget leak.
4. **One canonical path per entity.** No entity has two live rows.
5. **The page's directive and the table agree.** Impossible once the page reads
   the table, which is the point of having it read the table.

## Order of work

**1. Advertise what is already rankable.** Sitemap page depth, done
2026-09-21. Roughly 5,000 fixtures. Pure gain, no risk: these pages already
exist, already have predictions, already return `index`.

**2. Stop linking to `noindex` fixtures from hubs.** Recovers crawl budget
directly. Needs care -- the current design deliberately leaves `follow: true` on
those pages so they pass equity to teams and leagues, and dropping the links
outright forfeits that. The likely answer is that hubs list only fixtures with
predictions while the pages stay reachable by direct URL.

**3. Merge the 705 duplicate teams** (#121). Supervised: a rehearsal stalled the
ingestion worker.

**4. Introduce `seo_urls`** and move all three consumers onto it, which is what
stops steps 1-3 from being re-broken by the next well-meaning change.

Steps 1-3 are each worth doing alone. Step 4 is what makes them stay done.

## What this cannot do

It does not raise the crawl rate. That is Google's decision, made from site
quality, authority and speed. What it does is make sure the 275 crawls a day are
spent on pages that can rank, rather than on `noindex` pages, duplicates and
redirects.

It also does not fix thin content. A fixture page whose prediction is one market
with low confidence is indexable by this mechanism and may still be judged thin.
That is a product question, not a crawl question.
