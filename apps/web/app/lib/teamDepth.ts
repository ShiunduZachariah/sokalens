import type { CatalogMatch, TeamDetail } from "../data/catalog";

/**
 * Whether a team page has enough of its own content to be worth indexing, and
 * the computed figures that make it so.
 *
 * 414 team pages produced 3 clicks from 2,206 impressions -- a 0.14%
 * click-through rate, the largest impression sink on the property -- because
 * most of them were a crest, a competition name, and a list of fixtures. That
 * is the same page 414 times with the nouns swapped, which is precisely the
 * shape Google's scaled-content policy targets, and 316 of them landed in
 * "Crawled, currently not indexed".
 *
 * The answer is not to write 414 pages of prose. It is to compute figures that
 * are specific to each club from data the site already holds, and to withhold
 * the page from the index until there is something to compute them from. A
 * club with no played matches has nothing to say; saying it anyway is what
 * earned the 0.14%.
 */

/**
 * A team page needs at least this many settled results to compute anything.
 *
 * One is a deliberately low bar, and it is the bar the sitemap can prove: a
 * club with a settled fixture in its league feed necessarily has that same
 * fixture in its own. Setting it higher would put teams in `teams.xml` that
 * the page then noindexes, which is the exact contradiction the
 * `seo-invariants` gate exists to catch.
 *
 * What earns the indexation is the computed block below, not the count: goals
 * for and against per match, clean-sheet rate, BTTS rate and over-2.5 rate are
 * this club's numbers and no other's. Deepening the top clubs with real
 * editorial remains open (SEO-10, Wave 3).
 */
export const MINIMUM_INDEXABLE_MATCHES = 1;

export type TeamOutcome = "won" | "drawn" | "lost";

export type TeamForm = {
  matches: number;
  won: number;
  drawn: number;
  lost: number;
  goalsFor: number;
  goalsAgainst: number;
  cleanSheets: number;
  bothTeamsScored: number;
  overTwoFive: number;
};

function isHome(match: CatalogMatch, teamName: string) {
  return match.home_team?.trim().toLowerCase() === teamName.trim().toLowerCase();
}

function settled(match: CatalogMatch) {
  return typeof match.home_score === "number" && typeof match.away_score === "number";
}

/**
 * Form over the settled matches the feed carries.
 *
 * Only matches with both scores count: an unplayed or unrecorded fixture in
 * the denominator would make every figure on the page quietly wrong.
 */
export function teamForm(team: Pick<TeamDetail, "name" | "recent_matches">): TeamForm {
  const form: TeamForm = {
    matches: 0, won: 0, drawn: 0, lost: 0,
    goalsFor: 0, goalsAgainst: 0, cleanSheets: 0, bothTeamsScored: 0, overTwoFive: 0,
  };
  for (const match of team.recent_matches) {
    if (!settled(match)) continue;
    const home = isHome(match, team.name);
    const scored = home ? match.home_score! : match.away_score!;
    const conceded = home ? match.away_score! : match.home_score!;
    form.matches += 1;
    form.goalsFor += scored;
    form.goalsAgainst += conceded;
    if (scored > conceded) form.won += 1;
    else if (scored === conceded) form.drawn += 1;
    else form.lost += 1;
    if (conceded === 0) form.cleanSheets += 1;
    if (scored > 0 && conceded > 0) form.bothTeamsScored += 1;
    if (scored + conceded > 2.5) form.overTwoFive += 1;
  }
  return form;
}

/**
 * A team page earns indexation, or it does not get it.
 *
 * `follow` stays on either way: an unindexed team page is still a crawl path
 * to the fixtures it links, which is the job it was doing for the mesh anyway.
 */
export function isIndexableTeam(form: TeamForm) {
  return form.matches >= MINIMUM_INDEXABLE_MATCHES;
}

export function formatRate(part: number, total: number) {
  if (total <= 0) return "-";
  return `${Math.round((part / total) * 100)}%`;
}

export function formatAverage(total: number, matches: number) {
  if (matches <= 0) return "-";
  return (total / matches).toFixed(2);
}
