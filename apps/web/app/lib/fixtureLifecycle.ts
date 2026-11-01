/**
 * Where a fixture sits in its useful life, for indexing purposes.
 *
 * Every fixture URL has roughly a 72-hour window of real search demand and
 * then effectively none, but the URL persists — and the count compounds every
 * week. ~16,900 URLs were known to Google with 8,318 refused, because the
 * crawl allowance was being spent re-fetching matches played weeks ago instead
 * of the pages that had never been indexed at all.
 *
 * Retirement is `noindex, follow` on a page that still returns 200. It is
 * deliberately *not* a 404, a 410, or a redirect to a hub: mass redirects to a
 * parent are treated as soft 404s, and removing the page would break every
 * inbound link and sever the internal links that carry equity to the team,
 * league and head-to-head pages. `follow` is the point — the page keeps
 * passing value even once it stops competing for its own query.
 *
 * There is modest residual demand for "X vs Y result" for a short period after
 * a match, so retirement is staged rather than immediate.
 */
export type FixtureLifecycle = "upcoming" | "live" | "settled" | "archived";

/** How long a settled fixture stays indexable. */
export const SETTLED_INDEXABLE_DAYS = 14;

/**
 * How many settled matches each side needs before its fixture page is worth
 * advertising to a crawler.
 *
 * Deliberately the same number as MIN_MATCHES_FOR_RATES in `marketReads.ts`,
 * which is the bar the preview builder itself applies before it will state a
 * rate. Below it there is nothing specific to say: the page is a pick and two
 * form strings, which is the same page several thousand times with the nouns
 * swapped. That shape is what earned team pages 3 clicks from 2,206
 * impressions and left 316 of 414 in "Crawled, currently not indexed", and
 * `isIndexableTeam` was the fix; this is the same fix for fixtures.
 *
 * Mirrored in Go as MinimumFixtureHistory in
 * services/worker/internal/seourl/decide.go, because the sweep decides what the
 * sitemap advertises while the page decides its own robots tag. The two are
 * pinned together by a test -- a sitemap advertising a noindexed URL is the
 * defect the seourl package exists to prevent.
 */
export const MINIMUM_FIXTURE_HISTORY = 4;

/**
 * Whether a fixture has enough settled history behind it to be indexable.
 *
 * Both sides must clear the bar, not the pair on average: a fixture where one
 * club has forty matches on record and its opponent has none still produces a
 * preview that can say nothing about half the match.
 */
export function hasIndexableHistory(
  fixture: { homeHistoryCount?: number; awayHistoryCount?: number },
): boolean {
  return (fixture.homeHistoryCount ?? 0) >= MINIMUM_FIXTURE_HISTORY
    && (fixture.awayHistoryCount ?? 0) >= MINIMUM_FIXTURE_HISTORY;
}

const DAY_MS = 24 * 60 * 60 * 1000;

const LIVE_STATUSES = new Set(["live", "halftime", "second_half", "extra_time", "penalties", "break"]);
const FINISHED_STATUSES = new Set([
  "finished", "ft", "aet", "pen", "awarded", "walkover",
  "cancelled", "canceled", "postponed", "abandoned",
]);

/**
 * Whether a status means the match is on the pitch right now.
 *
 * Exported so the in-play surfaces share this one definition. The status list
 * had already been copied into the today page's filter; a third copy would be
 * the one that quietly forgets "break" or "penalties" and drops those matches
 * off the board while they are still being played.
 */
export function isLiveFixture(status: string): boolean {
  return LIVE_STATUSES.has(status?.toLowerCase?.() ?? "");
}

export function fixtureLifecycle(
  fixture: { kickoffAt: string; status: string },
  now: Date = new Date(),
): FixtureLifecycle {
  const status = fixture.status?.toLowerCase?.() ?? "";
  if (LIVE_STATUSES.has(status)) return "live";

  const kickoff = Date.parse(fixture.kickoffAt);
  // An unparseable kickoff is treated as upcoming rather than archived: losing
  // a page that should be indexed is worse than keeping one that should not.
  if (Number.isNaN(kickoff)) return "upcoming";

  if (kickoff > now.getTime()) return "upcoming";
  if (!FINISHED_STATUSES.has(status)) return "live";

  const ageDays = (now.getTime() - kickoff) / DAY_MS;
  return ageDays > SETTLED_INDEXABLE_DAYS ? "archived" : "settled";
}

/**
 * Archived fixtures are withheld from the index and from every sitemap; the
 * two decisions must agree, or the sitemap advertises a noindexed URL.
 */
export function isArchivedFixture(
  fixture: { kickoffAt: string; status: string },
  now: Date = new Date(),
): boolean {
  return fixtureLifecycle(fixture, now) === "archived";
}

/**
 * Whether a fixture page may be indexed.
 *
 * This is the page's whole robots decision, named and exported rather than
 * written inline in `generateMetadata`, for one reason: it is half of a
 * cross-language contract and the other half is in Go.
 *
 * `seourl.Decide` decides what the sitemap advertises; this decides what the
 * page tells a crawler. They must agree -- a sitemap advertising a noindexed
 * URL is the defect the seourl package exists to prevent -- and on 2026-09-28
 * they did not, because the history gate was flagged on one side and
 * unconditional on the other.
 *
 * Go is the source of truth. `packages/contracts/seo-indexability.golden.json`
 * is generated from `Decide`, and this function is tested against it, so a
 * change to the Go decision fails the web tests until the page follows. That
 * only works if the page and the test call the SAME code -- if the test
 * re-implemented this expression it would prove nothing about what the page
 * actually serves.
 */
export function isIndexableFixture(
  fixture: { kickoffAt: string; status: string; homeHistoryCount?: number; awayHistoryCount?: number },
  hasPrediction: boolean,
  now: Date = new Date(),
): boolean {
  return hasPrediction && !isArchivedFixture(fixture, now) && hasIndexableHistory(fixture);
}
