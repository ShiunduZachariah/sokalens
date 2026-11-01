import Link from "next/link";
import { leagueSlugForName } from "../data/leagueLookup";
import type { LiveMatch } from "../data/liveMatches";
import { leaguePredictionPath, teamPath } from "../lib/catalogSlug";
import { marketSeoPath } from "../lib/seo";
import { localePath } from "../i18n/config";
import { requestLocaleContext } from "../i18n/server";

/**
 * The crawl mesh on a fixture page.
 *
 * A sitemap announces that a URL exists; an internal link is the only thing
 * that says it matters, and it is the actual mechanism by which a deep page
 * gets crawled at all. 1,308 URLs sat in "Discovered, currently not indexed" --
 * Google knew where they were and did not think them worth fetching.
 *
 * Fixture pages are the deepest and most numerous surface on the property, so
 * they are where the mesh pays: each one links up to its league and date hub,
 * sideways to the market hub for its own prediction, and across to both teams.
 * That puts every hub within one click of thousands of pages, and no indexable
 * page more than three clicks from the homepage.
 *
 * Every href is resolved, never constructed: an unknown league yields no link
 * rather than a guessed slug that 404s.
 */

function dateHub(kickoffAt: string, now = new Date()) {
  const kickoff = new Date(kickoffAt);
  if (Number.isNaN(kickoff.getTime())) return null;
  const days = Math.round((Date.UTC(kickoff.getUTCFullYear(), kickoff.getUTCMonth(), kickoff.getUTCDate())
    - Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate())) / 86_400_000);
  if (days <= 0) return { path: "/predictions/today", label: "Today's predictions" };
  if (days === 1) return { path: "/predictions/tomorrow", label: "Tomorrow's predictions" };
  // Anything further out is reachable from the weekend hub, which is the only
  // other date hub that exists. Inventing /predictions/2026-09-14 here would
  // announce a route the app does not serve.
  return { path: "/predictions/weekend", label: "Weekend predictions" };
}

export async function FixtureLinkMesh({ match }: { match: LiveMatch }) {
  const { locale } = await requestLocaleContext();
  const prediction = match.prediction ?? match.predictions[0] ?? null;

  // The payload's own slug first. The name lookup is the fallback for an API
  // that predates league_slug; it is ambiguous across countries and was the
  // reason most fixture pages carried no league link.
  const leagueSlug = match.leagueSlug || await leagueSlugForName(match.leagueName);
  const links: { path: string; label: string }[] = [];

  const leagueHref = leaguePredictionPath(leagueSlug);
  if (leagueHref) links.push({ path: leagueHref, label: `${match.leagueName} predictions` });

  const hub = dateHub(match.kickoffAt);
  if (hub) links.push(hub);

  const marketHref = prediction ? marketSeoPath(prediction.marketKey, prediction.selection) : null;
  if (marketHref) links.push({ path: marketHref, label: "More of this market" });

  const homeHref = teamPath(match.homeTeamSlug);
  if (homeHref) links.push({ path: homeHref, label: `${match.homeTeam} fixtures` });
  const awayHref = teamPath(match.awayTeamSlug);
  if (awayHref) links.push({ path: awayHref, label: `${match.awayTeam} fixtures` });

  if (links.length === 0) return null;

  return (
    <nav aria-label="Related predictions" className="section fixture-mesh">
      <div className="section-header">
        <div>
          <h2>Related predictions</h2>
          <p>Every competition, market and team connected to this fixture.</p>
        </div>
      </div>
      <ul className="pill-row fixture-mesh__links">
        {links.map((link) => (
          <li key={link.path}>
            <Link className="button button--secondary" href={localePath(link.path, locale)}>{link.label}</Link>
          </li>
        ))}
      </ul>
    </nav>
  );
}
