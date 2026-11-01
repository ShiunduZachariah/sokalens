import Link from "next/link";
import { LocalTime } from "./LocalTime";
import { MatchupIdentity } from "./MatchupIdentity";
import { TeamCrest } from "./TeamCrest";
import { ChevronRightIcon } from "./match/icons";
import type { CatalogMatch } from "../data/catalog";
import type { Locale } from "../i18n/config";
import { localePath } from "../i18n/config";
import { fixtureHref } from "../lib/fixtureHref";
import { matchStatusLabel } from "../lib/matchStatus";

/**
 * Upcoming fixtures, grouped by local day.
 *
 * Grouping by day rather than listing flat is the design's choice on both the
 * league hub and the team page, and it is what makes the list scannable: a hub
 * returns the next few weeks, and a flat run of forty rows gives a reader no way
 * to find Saturday.
 *
 * The day label is formatted on the server from the kickoff instant, so it is in
 * UTC, while each row's time is a `LocalTime` in the reader's own zone. Those can
 * disagree at the edges of a day -- a 23:30 UTC Friday kickoff is Saturday in
 * Nairobi. The group heading is therefore written as the competition's matchday,
 * not as the reader's, which is the honest reading of a server-rendered,
 * edge-cached page: per-request formatting is impossible here, and the row time
 * is the figure a reader acts on.
 */
function dayKey(kickoffAt: string) {
  // Sorts correctly as a string and is stable regardless of locale.
  return kickoffAt.slice(0, 10);
}

function dayLabel(isoDay: string) {
  return new Intl.DateTimeFormat("en-GB", {
    timeZone: "UTC",
    weekday: "short",
    day: "numeric",
    month: "short",
  }).format(new Date(`${isoDay}T12:00:00Z`));
}

export function FixtureGroups({ locale, matches }: { locale: Locale; matches: CatalogMatch[] }) {
  const groups = new Map<string, CatalogMatch[]>();
  for (const match of matches) {
    // A fixture with no kickoff still has to appear: the provider publishes
    // these for cup rounds whose date is not drawn yet, and dropping them makes
    // a hub silently under-report its own coverage.
    const key = match.kickoff_at ? dayKey(match.kickoff_at) : "";
    groups.set(key, [...(groups.get(key) ?? []), match]);
  }

  return (
    <>
      {[...groups.entries()]
        .sort(([left], [right]) => (left === "" ? 1 : right === "" ? -1 : left.localeCompare(right)))
        .map(([key, dayMatches]) => (
          <section className="td-group" key={key || "tbc"}>
            <div className="td-group__head">
              <h3>{key ? dayLabel(key) : "Date to be confirmed"}</h3>
              <span className="td-group__count">
                {dayMatches.length} {dayMatches.length === 1 ? "fixture" : "fixtures"}
              </span>
            </div>
            <ul className="lg-fixtures">
              {dayMatches.map((match) => (
                <li key={match.id}>
                  <Link href={localePath(fixtureHref(match), locale)}>
                    <span className="lg-fixture__time">
                      {match.kickoff_at ? <LocalTime timeOnly value={match.kickoff_at} /> : "TBC"}
                    </span>
                    {/*
                      Stacked with a 20px crest per line, as
                      league-premier-league.html draws it: 1.4em of the row's
                      15px name.

                      This replaces `Crest`, which is a CIRCLE and is for
                      competitions only since task A3 -- a club is a shield. It
                      also replaces the inline `[crest] Home v Away [crest]`
                      shape, where the two names shared one truncation budget:
                      the UI-05 defect, here with the crests on the outside so a
                      long home name pushed the away crest off the row.
                    */}
                    <MatchupIdentity
                      awayTeam={match.away_team}
                      awayTeamLogo={match.away_team_logo}
                      className="lg-fixture__teams"
                      homeTeam={match.home_team}
                      homeTeamLogo={match.home_team_logo}
                      layout="stacked"
                      size={20}
                    />
                    {/*
                      The pending state the LeagueEPL artboard exists to show
                      (handoff 6.4): a fixture with no pick yet says so in its
                      own words rather than showing a status the reader did not
                      ask about. league-premier-league.html draws it muted at
                      14px/700 in the pick column, with the indicator slot left
                      empty.

                      Only an explicit "pending" gets the copy. An absent
                      prediction_state means the API expressed no opinion,
                      which is not the same answer and has no copy of its own.

                      A `published` fixture still shows its status here, not its
                      pick: the catalogue payload carries no selection, market
                      or confidence yet. That is task F3, and its hazards are
                      written down in the V31 plan.
                    */}
                    {match.prediction_state === "pending" ? (
                      <span className="lg-fixture__pending">Pick publishes closer to kick-off</span>
                    ) : (
                      <span className="lg-fixture__status">{matchStatusLabel(match.status)}</span>
                    )}
                    <span aria-hidden="true" className="lg-card__chevron"><ChevronRightIcon /></span>
                  </Link>
                </li>
              ))}
            </ul>
          </section>
        ))}
    </>
  );
}

/**
 * Finished fixtures, with the score in the middle.
 *
 * The score is the only thing a reader came to this list for, so the design puts
 * it between the two names and mirrors the home name rightward against it. That
 * is why this is a separate component rather than `FixtureGroups` with a flag:
 * the row is a different shape, not the same shape with a score bolted on.
 */
export function ResultRows({ locale, matches }: { locale: Locale; matches: CatalogMatch[] }) {
  return (
    <ul className="lg-results">
      {matches.map((match) => (
        <li key={match.id}>
          <Link href={localePath(fixtureHref(match), locale)}>
            <span className="lg-result__day">
              {match.kickoff_at ? dayLabel(dayKey(match.kickoff_at)) : "—"}
            </span>
            {/*
              Inline here, not stacked, and that is the artboard's call rather
              than mine: league-premier-league.html and team.html both draw a
              result row as `[crest] Home · score · [crest] Away` on one line,
              because the score is the row's subject and it belongs BETWEEN the
              two names. Stacking would put the score beside only one of them.

              The crest sits to the left of each name on both sides, including
              the right-aligned home side -- again as drawn.
            */}
            <span className="lg-result__teams">
              <span className="lg-result__home sl-team">
                <TeamCrest logoUrl={match.home_team_logo} name={match.home_team} size={20} />
                <span className="sl-teamname">{match.home_team}</span>
              </span>
              <span className="lg-result__score">
                {match.home_score ?? "–"}&ndash;{match.away_score ?? "–"}
              </span>
              <span className="lg-result__away sl-team">
                <TeamCrest logoUrl={match.away_team_logo} name={match.away_team} size={20} />
                <span className="sl-teamname">{match.away_team}</span>
              </span>
            </span>
            <span aria-hidden="true" className="lg-card__chevron"><ChevronRightIcon /></span>
          </Link>
        </li>
      ))}
    </ul>
  );
}
