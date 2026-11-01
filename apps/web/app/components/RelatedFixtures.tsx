import Link from "next/link";
import type { RelatedFixture } from "../lib/relatedFixtures";
import { fixtureHref } from "../lib/fixtureHref";
import { localePath, type Locale } from "../i18n/config";
import { LocalTime } from "./LocalTime";

/**
 * Links to other fixture pages. The anchor text names both teams and says
 * "prediction", because that is the query each target page answers; "View
 * match" tells a crawler nothing about where it leads.
 */
export function RelatedFixtures({ fixtures, leagueName, locale }: { fixtures: RelatedFixture[]; leagueName: string; locale: Locale }) {
  if (fixtures.length === 0) return null;
  return (
    <nav aria-labelledby="related-fixtures-title" className="section related-fixtures">
      <div className="section-header">
        <div>
          <h2 id="related-fixtures-title">More fixtures</h2>
          <p>What both teams play next, and upcoming {leagueName || "competition"} matches.</p>
        </div>
      </div>
      <ul className="related-fixtures__list">
        {fixtures.map(({ match }) => (
          <li key={match.id}>
            <Link href={localePath(fixtureHref(match), locale)}>
              {match.home_team} vs {match.away_team} prediction
            </Link>
            <span className="micro">
              {match.league_name}
              {match.kickoff_at ? <> · <LocalTime value={match.kickoff_at} /></> : null}
            </span>
          </li>
        ))}
      </ul>
    </nav>
  );
}
