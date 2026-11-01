import type { CatalogMatch } from "../data/catalog";

/**
 * Which other fixtures a fixture page links to.
 *
 * URL Inspection on a live fixture reported "Referring page: None detected":
 * Google knew the URL only from the sitemap, and a URL nothing links to is one
 * it has no reason to fetch. Fixture pages are the most numerous surface on
 * the site, so linking them to each other is what turns ~1,500 sitemap entries
 * into a mesh Google can walk.
 *
 * Two sources, in order of how likely a reader is to want them:
 *
 *   1. Each team's next match. The durable thread: it survives this fixture
 *      being played, and it is what a reader of a finished match wants next.
 *   2. The same competition's upcoming fixtures, nearest first. What a reader
 *      comparing a matchday actually clicks.
 *
 * "Same market in other leagues" was considered and left to the market hub the
 * link mesh already points at: the only feed for it pages through a whole day
 * of predictions, which is too expensive to do on every fixture render.
 */

export const MAX_RELATED_FIXTURES = 6;
export const MAX_LEAGUE_FIXTURES = 4;

export type RelatedFixture = { match: CatalogMatch; reason: "team" | "league" };

type Input = {
  matchId: number;
  homeNext?: CatalogMatch | null;
  awayNext?: CatalogMatch | null;
  leagueUpcoming?: CatalogMatch[] | null;
  now?: Date;
};

function kickoff(match: CatalogMatch) {
  const at = Date.parse(match.kickoff_at);
  return Number.isNaN(at) ? Number.POSITIVE_INFINITY : at;
}

export function relatedFixtures({ matchId, homeNext, awayNext, leagueUpcoming, now = new Date() }: Input): RelatedFixture[] {
  const seen = new Set<number>([matchId]);
  const out: RelatedFixture[] = [];
  const add = (match: CatalogMatch | null | undefined, reason: RelatedFixture["reason"]) => {
    if (!match || !Number.isFinite(match.id) || seen.has(match.id)) return;
    if (!match.home_team || !match.away_team || !match.league_name) return;
    seen.add(match.id);
    out.push({ match, reason });
  };

  add(homeNext, "team");
  add(awayNext, "team");

  const upcoming = (leagueUpcoming ?? [])
    .filter((match) => kickoff(match) >= now.getTime())
    .sort((a, b) => kickoff(a) - kickoff(b) || a.id - b.id);
  let fromLeague = 0;
  for (const match of upcoming) {
    if (fromLeague >= MAX_LEAGUE_FIXTURES || out.length >= MAX_RELATED_FIXTURES) break;
    const before = out.length;
    add(match, "league");
    if (out.length > before) fromLeague += 1;
  }
  return out.slice(0, MAX_RELATED_FIXTURES);
}
