import { seoFixturePath } from "./seo.ts";
import { validEntityId, validRouteText } from "./catalogSlug.ts";

/**
 * The one internal URL for a fixture.
 *
 * Every fixture used to have two: `/matches/{id}` and
 * `/predictions/{league}/{home}-vs-{away}-{id}`. 5,306 pages -- 64% of every
 * non-indexed URL on the property -- sat on the first shape, still internally
 * linked and still crawled, while the second was the indexable twin. The
 * shadow route now issues a permanent redirect, so an internal link to it is a
 * link to a redirect: it costs a hop, leaks crawl budget, and the
 * `seo-invariants` CI job fails the build over it.
 *
 * Call this instead of writing a fixture href by hand. The `/matches/{id}`
 * fallback exists only for the case where the caller genuinely has an id and
 * nothing else; prefer passing the names.
 */
export type FixtureHrefInput = {
  id?: number | string | null;
  matchId?: number | string | null;
  leagueName?: string | null;
  league_name?: string | null;
  homeTeam?: string | null;
  home_team?: string | null;
  awayTeam?: string | null;
  away_team?: string | null;
  /**
   * "Home vs Away", used only when the feed carries the pairing as one string
   * and not as two fields. The canonical slug is built from the same two names
   * either way, so splitting here yields the same URL the fixture page
   * redirects to rather than a hop through the retired route.
   */
  matchTitle?: string | null;
};

function teamsFromTitle(title: string | null | undefined) {
  const parts = title?.split(/\s+vs\.?\s+/i) ?? [];
  return parts.length === 2 ? { home: parts[0].trim(), away: parts[1].trim() } : null;
}

export function fixtureHref(input: FixtureHrefInput): string {
  /*
   * `matchId` first, and the order is the whole correctness of this function.
   *
   * A MATCH object's `id` is the match id, so reading `id` worked there. A
   * PREDICTION object carries its own `id` -- the prediction id -- next to
   * `matchId`, and reading that built a URL for a match that does not exist.
   * On 2026-10-03 the #1 Best Bet linked to `...-2935987`, which is a
   * prediction id, and 404'd; the same fixture is match 5133154. The homepage
   * was right only because it had already worked around this by calling
   * `predictionFixturePath`, which takes `matchId` explicitly -- a fix applied
   * in one place and never generalised.
   *
   * `??` also only falls back on null and undefined, so `id: 0` won as well.
   * ACCA legs parse `num(raw.id ?? raw.prediction_id)` against an API that
   * sends a UUID, so every leg had `id: 0`, failed `validEntityId`, and linked
   * to `/predictions/today` instead of its match -- the same bug returning 200
   * instead of 404, which is why nothing caught it.
   *
   * `matchId` is unambiguous wherever it exists, and match objects do not carry
   * it, so preferring it fixes nine call sites without touching any of them.
   */
  const id = Number(input.matchId ?? input.id);
  if (!validEntityId(id)) return "/predictions/today";

  const fromTitle = teamsFromTitle(input.matchTitle);
  const leagueName = input.leagueName ?? input.league_name;
  const homeTeam = input.homeTeam ?? input.home_team ?? fromTitle?.home;
  const awayTeam = input.awayTeam ?? input.away_team ?? fromTitle?.away;
  if (!validRouteText(leagueName) || !validRouteText(homeTeam) || !validRouteText(awayTeam)) {
    return `/matches/${id}`;
  }
  return seoFixturePath({ id, leagueName, homeTeam, awayTeam });
}
