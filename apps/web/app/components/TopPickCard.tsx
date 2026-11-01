import Link from "next/link";
import { LocalTime } from "./LocalTime";
import { MatchupIdentity } from "./MatchupIdentity";
import type { Locale } from "../i18n/config";
import { localePath } from "../i18n/config";

export type TopPick = {
  href: string;
  homeTeam: string;
  awayTeam: string;
  /** Provider crests; absent falls through to the initials shield. */
  homeTeamLogo?: string;
  awayTeamLogo?: string;
  league: string;
  kickoffAt: string | null;
  market: string;
  selection: string;
  confidence: number;
};

/**
 * One ranked pick, numbered, above a prediction list.
 *
 * The rank is drawn rather than left to the `<ol>` marker because it is the
 * point of the block: this list exists on `/predictions/tomorrow` because that
 * page earned 1,033 impressions and one click at average position 82.9, and the
 * diagnosis was never ranking -- it was that nothing in the result gave a reader
 * a reason to choose it. A named pick with a number against it does.
 *
 * `LocalTime` rather than a server-formatted string: the kickoff has to read in
 * the reader's own zone, and the server does not know it on first render.
 */
export function TopPickCard({ locale, pick, rank }: { locale: Locale; pick: TopPick; rank: number }) {
  return (
    <li className="td-toppick">
      <Link className="td-toppick__link" href={localePath(pick.href, locale)}>
        <span aria-hidden="true" className="td-toppick__rank">{rank}</span>
        <span className="td-toppick__fixture">
          {/*
            Inline, and that is a deliberate exception rather than an oversight.
            UI-04 says every team name carries its crest, so these gain one; but
            this block has no V31 artboard -- it is a SokaLens SEO addition, not
            a design surface -- and UI-05's stacking is specified for the shared
            PredictionRow, which this is not. Ten shortlist rows sit directly
            above the full list on /predictions/tomorrow, and stacking them
            would add ~100px before a reader reaches it. So: crests yes, relayout
            no, until a design draws this block.
          */}
          <MatchupIdentity
            awayTeam={pick.awayTeam}
            awayTeamLogo={pick.awayTeamLogo}
            className="td-toppick__teams"
            homeTeam={pick.homeTeam}
            homeTeamLogo={pick.homeTeamLogo}
            layout="inline"
            size={20}
          />
          <span className="td-toppick__meta">
            {pick.league}
            {/*
              A fixture whose kickoff the provider has not confirmed shows the
              league alone rather than "Kickoff TBC" against the separator: these
              are ranked picks, and a row reading "Premier League · Kickoff TBC"
              in the top ten reads as a data fault.
            */}
            {pick.kickoffAt ? <> · <LocalTime value={pick.kickoffAt} /></> : null}
          </span>
        </span>
        <span className="td-toppick__pick">
          <strong>{pick.selection}</strong>
          <span className="td-toppick__meta">{pick.market}</span>
        </span>
        <span className="td-toppick__conf">{Math.round(pick.confidence)}%</span>
      </Link>
    </li>
  );
}
