import Link from "next/link";

import { matchClockLabel, matchStatusLabel, type LiveMatch } from "../data/liveMatches";
import { compareCompetitions, countryLabel, footballCountry } from "../lib/competitionOrder";
import { fixtureHref } from "../lib/fixtureHref";
import { pluralize } from "../lib/format";
import { CompactMatchTimeline } from "./MatchTimeline";
import { TeamCrest } from "./TeamCrest";
import { ChevronRightIcon } from "./match/icons";
import { TrackerChip } from "./TrackerChip";
import { marketSelectionLabel } from "../data/livePredictions";
import type { Locale } from "../i18n/config";

/**
 * The in-play board: every match that is on the pitch right now.
 *
 * Deliberately a server component with no client state. The match clock is
 * server-derived from the provider's elapsed minute rather than counted up in
 * the browser, because a browser-side timer keeps ticking when the feed stops
 * and would show a confident 73' for a match nobody is receiving updates about.
 * Freshness comes from the 15-second revalidate the match data already uses,
 * so a stale board goes stale visibly rather than silently.
 */

type InPlayBoardProps = {
  /** For the market-selection label on each row's pre-match pick. */
  locale: Locale;
  matches: LiveMatch[];
};

type LeagueGroup = {
  league: string;
  matches: LiveMatch[];
};

type CountryGroup = {
  country: string;
  leagues: LeagueGroup[];
};

function groupByCompetition(matches: LiveMatch[]): CountryGroup[] {
  const byCountry = new Map<string, Map<string, LiveMatch[]>>();
  for (const match of matches) {
    const country = footballCountry(match.country, match.leagueName, match.leagueSlug);
    const league = match.leagueName;
    const leagues = byCountry.get(country) ?? new Map<string, LiveMatch[]>();
    leagues.set(league, [...(leagues.get(league) ?? []), match]);
    byCountry.set(country, leagues);
  }

  return [...byCountry.entries()]
    .map(([country, leagues]) => ({
      country,
      leagues: [...leagues.entries()]
        .map(([league, leagueMatches]) => ({
          league,
          // Latest kick-offs first within a league: the match furthest along is
          // usually the one being watched.
          matches: [...leagueMatches].sort((a, b) => (b.statusElapsed ?? 0) - (a.statusElapsed ?? 0)),
        }))
        .sort((a, b) => a.league.localeCompare(b.league)),
    }))
    // compareCompetitions ranks by popularity first, so the leagues people
    // actually came for sit above a long tail of lower divisions. Comparing on
    // the group's first league keeps a country's position stable.
    .sort((a, b) =>
      compareCompetitions(
        { country: a.country, league: a.leagues[0]?.league ?? "" },
        { country: b.country, league: b.leagues[0]?.league ?? "" },
      ),
    );
}

export function InPlayBoard({ locale, matches }: InPlayBoardProps) {
  const groups = groupByCompetition(matches);

  if (groups.length === 0) {
    return (
      <div className="ac-empty" aria-live="polite">
        <h3>Nothing in play</h3>
        <p>No matches are being played right now. The board fills as kick-offs arrive.</p>
      </div>
    );
  }

  return (
    <>
      {groups.map(({ country, leagues }) => (
        <section className="td-group" key={country}>
          <div className="td-group__head">
            <h2>{countryLabel(country)}</h2>
            <span className="td-group__count">
              {pluralize(leagues.reduce((total, item) => total + item.matches.length, 0), "match", "matches")}
            </span>
          </div>
          {leagues.map(({ league, matches: leagueMatches }) => (
            <div key={`${country}-${league}`}>
              <div className="ip-leaguehead">
                <h3>{league}</h3>
                <span>{pluralize(leagueMatches.length, "match", "matches")}</span>
              </div>
              <ol className="ip-rows">
                {leagueMatches.map((match) => {
                  /*
                    Whichever side is ahead is drawn in full strength and the
                    other is dimmed, so the result of a row is legible without
                    reading the digits. Level is neither: dimming both would
                    make a 1-1 look like a match nobody is winning rather than
                    one both are.
                  */
                  const home = match.homeScore ?? null;
                  const away = match.awayScore ?? null;
                  const leading = home != null && away != null && home !== away
                    ? (home > away ? "home" : "away")
                    : null;
                  const sideClass = (side: "home" | "away") =>
                    leading === null ? "ip-side" : leading === side ? "ip-side ip-side--lead" : "ip-side ip-side--trail";
                  return (
                    <li key={match.id}>
                      <Link href={fixtureHref(match)}>
                        <span className="ip-clock">
                          <strong>{matchClockLabel(match)}</strong>
                          <span>{matchStatusLabel(match)}</span>
                        </span>
                        {/*
                          A live match without a score is a real state, not an
                          error: the provider can report a kick-off before it
                          reports 0-0. An en dash keeps the row honest rather
                          than printing a zero nobody sent.
                        */}
                        {/*
                          Each side carries its crest at 20px, which is what
                          in-play.html draws: 1.4em of the row's 15px name.
                          The crest goes INSIDE the name cell rather than in a
                          column of its own, so the two scores stay in one
                          column a reader can compare -- the reason this is a
                          two-by-two grid in the first place.

                          `sl-teamname` holds the ellipsis, not `ip-side`: with
                          the crest in the same box, truncating the box would
                          eventually clip the crest instead of the name.
                        */}
                        <span className="ip-score">
                          <span className={`${sideClass("home")} sl-team`}>
                            <TeamCrest logoUrl={match.homeTeamLogo} name={match.homeTeam} size={20} />
                            <span className="sl-teamname">{match.homeTeam}</span>
                          </span>
                          <span className={`${sideClass("home")} ip-score__n`}>{home ?? "\u2013"}</span>
                          <span className={`${sideClass("away")} sl-team`}>
                            <TeamCrest logoUrl={match.awayTeamLogo} name={match.awayTeam} size={20} />
                            <span className="sl-teamname">{match.awayTeam}</span>
                          </span>
                          <span className={`${sideClass("away")} ip-score__n`}>{away ?? "\u2013"}</span>
                        </span>
                        {/*
                          Goals and dismissals only, and nothing at all for a
                          goalless match: the board's job is to show dozens of
                          scores at once, and a full timeline per row buries
                          them. The match page carries the complete one.
                        */}
                        {/*
                          The pre-match pick and how it is standing, from the
                          engine's own tracker. `tracker_state` is on the headline
                          prediction of every match list as of the 2026-10-02 API
                          change, so this costs no extra request -- the board was
                          already fetching it and throwing it away.

                          TrackerChip renders nothing for `null` (nothing has
                          judged the pick) or `unknown` (judged, undecidable), so a
                          match with no pre-match pick shows its score and nothing
                          else -- which is the row the design added for exactly
                          this case.
                        */}
                        <span className="ip-pick">
                          {match.prediction ? (
                            <>
                              <span className="ip-pick__selection">
                                {marketSelectionLabel(match.prediction.marketKey, match.prediction.selection, locale)}
                              </span>
                              <TrackerChip state={match.prediction.trackerState} />
                            </>
                          ) : null}
                        </span>
                        <span className="ip-timeline">
                          <CompactMatchTimeline events={match.timeline} />
                        </span>
                        <span aria-hidden="true" className="lg-card__chevron"><ChevronRightIcon /></span>
                      </Link>
                    </li>
                  );
                })}
              </ol>
            </div>
          ))}
        </section>
      ))}
    </>
  );
}
